package onboardsetup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHailoContainerCheckDecodesQuotedAtlasEnvironment(t *testing.T) {
	temporary := t.TempDir()
	bin := filepath.Join(temporary, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	commands := map[string]string{
		"dpkg-query": `#!/bin/sh
for package; do :; done
case "$package" in
    hailort) printf '%s' '4.20.0-1' ;;
    hailo-tappas-core) printf '%s' '3.31.0+1-1' ;;
    *) exit 1 ;;
esac
`,
		"gst-inspect-1.0": "#!/bin/sh\nexit 0\n",
		"python3":         "#!/bin/sh\nexit 0\n",
		"hailortcli": `#!/bin/sh
case "$1" in
    fw-control)
        printf '%s\n' 'Device Architecture: HAILO8L' 'Firmware Version: 4.20.0'
        ;;
    parse-hef)
        printf '%s\n' 'Architecture HEF was compiled for: HAILO8L'
        ;;
    *)
        exit 1
        ;;
esac
`,
	}
	for name, content := range commands {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	model := filepath.Join(temporary, "objects.hef")
	if err := os.WriteFile(model, []byte("hef"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "packaging", "hailo", "atlas-hailo-container-check")
	command := exec.Command("/bin/sh", script)
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"ATLAS_HAILO_DEVICE_PATH=/dev/null",
		`ATLAS_HAILO_ACCELERATOR="hailo-8l"`,
		`ATLAS_PERCEPTION_MODEL_PATH="`+model+`"`,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("container check failed: %v\n%s", err, output)
	}
	for _, expected := range []string{
		"MODEL_READY=true",
		"MODEL_ARCHITECTURE=hailo-8l",
		"MODEL_COMPATIBLE=true",
	} {
		if !strings.Contains(string(output), expected) {
			t.Fatalf("output = %q, want %q", output, expected)
		}
	}
}
