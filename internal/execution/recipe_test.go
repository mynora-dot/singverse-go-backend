package execution

import "testing"

func TestAIBackendSelectionKeepsCPUForFallback(t *testing.T) {
	for _, profile := range []string{legacyProfile, "AI_CLEAN"} {
		for _, backend := range []string{"cpu", "openvino-cpu"} {
			t.Run(profile+"/"+backend, func(t *testing.T) {
				if err := (audioRecipe{profile: profile}).validateBackend(backend, "SEPARATE"); err == nil {
					t.Fatal("CPU must not be an independently selectable AI backend")
				}
			})
		}
		for _, backend := range []string{"", "auto", "cuda", "openvino", "rknn", "mlx"} {
			if err := (audioRecipe{profile: profile}).validateBackend(backend, "SEPARATE"); err != nil {
				t.Fatalf("%s/%s: %v", profile, backend, err)
			}
		}
	}
	for _, spec := range []audioRecipe{{profile: "DSP_FAST"}, {profile: "AI_CLEAN", source: "stream:1"}} {
		if err := spec.validateBackend("cpu", "SEPARATE"); err != nil {
			t.Fatalf("Source reuse and DSP still execute on CPU: %v", err)
		}
	}
	for _, kind := range []string{"FAST", "PROBE"} {
		if err := (audioRecipe{profile: legacyProfile}).validateBackend("cpu", kind); err != nil {
			t.Fatalf("%s does not run AI separation: %v", kind, err)
		}
	}
}
