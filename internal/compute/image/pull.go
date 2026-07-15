// Copyright © 2023 FORTH-ICS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package image

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"hpk/internal/compute"
	"hpk/pkg/process"
)

// ResolveLocal resolves an image reference to a local SIF file without pulling.
func ResolveLocal(imageDir string, imageName string) (*Image, error) {
	if strings.HasPrefix(imageName, "/") {
		if _, err := os.Stat(imageName); err != nil {
			return nil, fmt.Errorf("local image '%s' not found: %w", imageName, err)
		}

		return &Image{Filepath: imageName}, nil
	}

	imageName = strings.Split(imageName, "@")[0]
	img := &Image{Filepath: imageDir + ParseImageName(imageName)}

	file, err := os.Stat(img.Filepath)
	if err != nil {
		return nil, fmt.Errorf("local image '%s' not found: %w", img.Filepath, err)
	}

	if !file.Mode().IsRegular() {
		return nil, fmt.Errorf("imagePath '%s' is not a regular file", img.Filepath)
	}

	return img, nil
}

var pullLocks sync.Map

func getPullLock(targetPath string) *sync.Mutex {
	l, _ := pullLocks.LoadOrStore(targetPath, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func Pull(imageDir string, transport Transport, imageName string) (*Image, error) {
	img, err := ResolveLocal(imageDir, imageName)
	if err == nil {
		return img, nil
	}

	// Remove the digest form the image, because Apptainer fails with
	// "Docker references with both a tag and digest are currently not supported".
	imageName = strings.Split(imageName, "@")[0]

	img = &Image{Filepath: imageDir + ParseImageName(imageName)}

	lock := getPullLock(img.Filepath)
	lock.Lock()
	defer lock.Unlock()

	// Re-check after acquiring lock in case another goroutine completed the pull.
	if checkedImg, err := ResolveLocal(imageDir, imageName); err == nil {
		return checkedImg, nil
	}

	tmpFile := img.Filepath + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	// otherwise, download a fresh copy
	compute.DefaultLogger.Info(" * Downloading image...", "image", imageName, "dir", imageDir)
	if _, err := executePullWithProgress(
		imageName,
		compute.Environment.ApptainerBin,
		"pull",
		"--arch", runtime.GOARCH,
		tmpFile,
		transport.Wrap(imageName),
	); err != nil {
		_ = os.Remove(tmpFile)
		return nil, fmt.Errorf("downloading has failed: %w", err)
	}

	if err := os.Rename(tmpFile, img.Filepath); err != nil {
		_ = os.Remove(tmpFile)
		return nil, fmt.Errorf("failed to rename downloaded image: %w", err)
	}

	compute.DefaultLogger.Info(" * Download completed", "image", imageName, "path", img.Filepath)

	return img, nil
}

func executePullWithProgress(imageName string, command string, arguments ...string) ([]byte, error) {
	cmd := exec.Command(command, arguments...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, process.GoEnviron...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("could not prepare stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("could not prepare stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start process: %w", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	output := &bytes.Buffer{}

	progressRe := regexp.MustCompile(`(\d{1,3})%`)
	lastBucket := -1
	lastPercent := -1

	reportProgress := func(p int) {
		if p < 0 || p > 100 {
			return
		}

		bucket := p / 5
		if p != 100 && bucket <= lastBucket {
			return
		}

		lastBucket = bucket
		lastPercent = p

		const width = 20
		filled := p * width / 100
		bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
		compute.DefaultLogger.Info(" * Pull progress", "image", imageName, "progress", fmt.Sprintf("%3d%% [%s]", p, bar))
	}

	scanStream := func(r io.Reader) {
		defer wg.Done()

		scanner := bufio.NewScanner(r)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			mu.Lock()
			output.WriteString(line)
			output.WriteByte('\n')
			mu.Unlock()

			matches := progressRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				p, convErr := strconv.Atoi(m[1])
				if convErr != nil {
					continue
				}
				reportProgress(p)
			}
		}

		if err := scanner.Err(); err != nil {
			mu.Lock()
			output.WriteString(err.Error())
			output.WriteByte('\n')
			mu.Unlock()
		}
	}

	wg.Add(2)
	go scanStream(stdout)
	go scanStream(stderr)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	start := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			wg.Wait()

			mu.Lock()
			out := output.Bytes()
			mu.Unlock()

			if err != nil {
				return out, fmt.Errorf("process error: %w\noutput: %s", err, string(out))
			}

			if lastPercent >= 0 && lastPercent < 100 {
				reportProgress(100)
			}

			return out, nil

		case <-ticker.C:
			if lastPercent < 0 {
				compute.DefaultLogger.Info(" * Pull still in progress", "image", imageName, "elapsed", time.Since(start).Round(time.Second).String())
			}
		}
	}
}

func ParseImageName(rawImageName string) string {
	if rawImageName == "" {
		return "/unnamed.sif"
	}

	// Remove digest (@sha256:...)
	nameWithoutDigest := strings.Split(rawImageName, "@")[0]

	// Split name and tag
	parts := strings.Split(nameWithoutDigest, ":")
	imageRef := parts[0]
	tag := "latest"
	if len(parts) > 1 {
		tag = parts[1]
	}

	// Clean imageRef and tag by replacing non-alphanumeric characters with underscores
	reg := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	cleanRef := reg.ReplaceAllString(imageRef, "_")
	cleanTag := reg.ReplaceAllString(tag, "_")

	return "/" + cleanRef + "_" + cleanTag + ".sif"
}
