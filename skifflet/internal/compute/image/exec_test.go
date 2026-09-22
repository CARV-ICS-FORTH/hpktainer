package image_test

import (
	"testing"

	"skifflet/internal/compute"
	"skifflet/internal/compute/runtime"
)

func Test_ExecAsFakeroot(t *testing.T) {
	if runtime.DefaultPauseImage == nil {
		t.Skip("DefaultPauseImage not initialized (apptainer unavailable)")
	}

	tests := []struct {
		name    string
		cmd     []string
		wantErr bool
	}{
		{
			name:    "create directory",
			cmd:     []string{"mkdir", compute.Skiff.String() + "/pirate"},
			wantErr: false,
		},
		{
			name:    "touch files",
			cmd:     []string{"touch", compute.Skiff.String() + "/pirate/hook"},
			wantErr: false,
		},
		{
			name:    "ls files",
			cmd:     []string{"ls", "-lah", compute.Skiff.String()},
			wantErr: false,
		},
		{
			name:    "remove files",
			cmd:     []string{"rm", "-rf", compute.Skiff.String() + "/pirate"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := runtime.DefaultPauseImage.FakerootExec(nil, tt.cmd); (err != nil) != tt.wantErr {
				t.Errorf("ExecAsFakeroot() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
