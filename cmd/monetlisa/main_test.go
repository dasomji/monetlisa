package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearMonetlisaEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MONETLISA_MODEL", "MONETLISA_PROMPT", "MONETLISA_REASONING_EFFORT",
		"MONETLISA_HOME", "MONETLISA_SKILLS_DIR", "MONETLISA_DEFAULT_OUTPUT_PATH",
		"MONETLISA_SIZE", "MONETLISA_QUALITY", "MONETLISA_FORMAT", "MONETLISA_BACKGROUND", "CODEX_HOME",
	} {
		t.Setenv(key, "")
	}
}

func TestCodexRequestBodyUsesMessageListInput(t *testing.T) {
	body := codexRequestBody(config{
		model:           "gpt-5.5-mini",
		prompt:          "Improve it",
		input:           "Tell me a joke",
		reasoningEffort: "low",
	})

	input, ok := body["input"].([]map[string]any)
	if !ok {
		t.Fatalf("input has type %T, want []map[string]any", body["input"])
	}
	if len(input) != 1 || input[0]["role"] != "user" {
		t.Fatalf("input = %#v, want one user message", input)
	}
	content, ok := input[0]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content has type %T, want []map[string]any", input[0]["content"])
	}
	if len(content) != 1 || content[0]["type"] != "input_text" || content[0]["text"] != "Tell me a joke" {
		t.Fatalf("content = %#v, want input_text input", content)
	}
}

func TestCodexRequestBodyIncludesInputImages(t *testing.T) {
	body := codexRequestBody(config{
		model:  "gpt-5.5",
		prompt: "Improve it",
		input:  "Make this dashboard cleaner",
		action: "edit",
		inputImages: []inputImage{{
			ImageURL: "data:image/png;base64,Zm9v",
			Detail:   "high",
		}},
	})

	input := body["input"].([]map[string]any)
	content := input[0]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v, want text and image", content)
	}
	image := content[1]
	if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,Zm9v" || image["detail"] != "high" {
		t.Fatalf("image content = %#v", image)
	}
	tools := body["tools"].([]map[string]any)
	if tools[0]["action"] != "edit" {
		t.Fatalf("tool action = %#v, want edit", tools[0]["action"])
	}
}

func TestDefaultSettingsUseGPT55WithoutReasoning(t *testing.T) {
	clearMonetlisaEnv(t)
	cfg, err := parseRunFlags([]string{"--monetlisa-home", t.TempDir(), "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.model != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", cfg.model)
	}
	if cfg.reasoningEffort != "" {
		t.Fatalf("reasoning = %q, want disabled", cfg.reasoningEffort)
	}
	body := codexRequestBody(cfg)
	if _, ok := body["reasoning"]; ok {
		t.Fatalf("body included reasoning: %#v", body["reasoning"])
	}
	if body["stream"] != true {
		t.Fatalf("codex request stream = %#v, want true", body["stream"])
	}
	tools, ok := body["tools"].([]map[string]any)
	if !ok || len(tools) != 1 || tools[0]["type"] != "image_generation" {
		t.Fatalf("tools = %#v, want image_generation tool", body["tools"])
	}
}

func TestConfigFileOverridesDefaults(t *testing.T) {
	clearMonetlisaEnv(t)
	monetlisaHome := t.TempDir()
	model := "gpt-5.5-mini"
	reasoning := "low"
	if err := saveSettings(settingsPath(monetlisaHome), userSettings{Model: &model, ReasoningEffort: &reasoning}); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseRunFlags([]string{"--monetlisa-home", monetlisaHome, "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.model != model || cfg.reasoningEffort != reasoning {
		t.Fatalf("model/reasoning = %q/%q, want %q/%q", cfg.model, cfg.reasoningEffort, model, reasoning)
	}
}

func TestOutputPathFlagDefaultsRelativeToCurrentDirectory(t *testing.T) {
	clearMonetlisaEnv(t)
	cfg, err := parseRunFlags([]string{"--monetlisa-home", t.TempDir(), "--output", "images/cat.png", "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if got := effectiveOutputPath(cfg); got != "images/cat.png" {
		t.Fatalf("output path = %q", got)
	}
}

func TestConfigDefaultOutputPath(t *testing.T) {
	clearMonetlisaEnv(t)
	monetlisaHome := t.TempDir()
	path := "art/default.png"
	if err := saveSettings(settingsPath(monetlisaHome), userSettings{DefaultOutputPath: &path}); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseRunFlags([]string{"--monetlisa-home", monetlisaHome, "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if got := effectiveOutputPath(cfg); got != path {
		t.Fatalf("default output path = %q", got)
	}
}

func TestParseRunFlagsLoadsInputImage(t *testing.T) {
	clearMonetlisaEnv(t)
	imagePath := filepath.Join(t.TempDir(), "screenshot.png")
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := parseRunFlags([]string{"--monetlisa-home", t.TempDir(), "--image", imagePath, "--action", "edit", "Modernize this UI"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.inputImages) != 1 {
		t.Fatalf("inputImages = %#v", cfg.inputImages)
	}
	if !strings.HasPrefix(cfg.inputImages[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("image URL = %q", cfg.inputImages[0].ImageURL)
	}
	if cfg.inputImages[0].Detail != "high" {
		t.Fatalf("detail = %q, want high", cfg.inputImages[0].Detail)
	}
}

func TestParseRunFlagsRejectsEditWithoutImage(t *testing.T) {
	clearMonetlisaEnv(t)
	_, err := parseRunFlags([]string{"--monetlisa-home", t.TempDir(), "--action", "edit", "Modernize this UI"})
	if err == nil || !strings.Contains(err.Error(), "edit requires") {
		t.Fatalf("err = %v, want edit requires image", err)
	}
}

func TestExtractImageBase64FromSSE(t *testing.T) {
	input := []byte(`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aW1n"}}`)
	got, err := extractImageBase64(input)
	if err != nil {
		t.Fatal(err)
	}
	if got != "aW1n" {
		t.Fatalf("image = %q", got)
	}
}

func TestDefaultGeneratedOutputPathUsesWebP(t *testing.T) {
	path := defaultGeneratedOutputPath(config{format: "webp"})
	if !strings.HasSuffix(path, ".webp") {
		t.Fatalf("path = %q, want .webp", path)
	}
}

func TestParseRunFlagsRejectsTransparentBackground(t *testing.T) {
	clearMonetlisaEnv(t)
	_, err := parseRunFlags([]string{"--monetlisa-home", t.TempDir(), "--background", "transparent", "cat"})
	if err == nil || !strings.Contains(err.Error(), "transparent is not supported") {
		t.Fatalf("err = %v, want transparent unsupported", err)
	}
}

func TestFastDefaultsToPriorityForCodex(t *testing.T) {
	clearMonetlisaEnv(t)
	cfg, err := parseRunFlags([]string{"--monetlisa-home", t.TempDir(), "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	body := codexRequestBody(cfg)
	if got := body["service_tier"]; got != "priority" {
		t.Fatalf("service_tier = %#v, want priority", got)
	}
}

func TestPrintStatusShowsDefaults(t *testing.T) {
	clearMonetlisaEnv(t)
	cfg, skills, err := statusConfig([]string{"--monetlisa-home", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printStatus(&out, cfg, skills)
	got := out.String()
	if !strings.Contains(got, "model: gpt-5.5") || !strings.Contains(got, "format: webp") || !strings.Contains(got, "quality: auto") || !strings.Contains(got, "fast: true") {
		t.Fatalf("status output missing defaults:\n%s", got)
	}
}

func TestParseRunFlagsLoadsSkillPromptByMarkdownFilename(t *testing.T) {
	clearMonetlisaEnv(t)
	monetlisaHome := t.TempDir()
	skillsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillsDir, "transcript.md"), []byte("Clean up this transcript."), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := parseRunFlags([]string{"--monetlisa-home", monetlisaHome, "--skills-dir", skillsDir, "--transcript", "Hello world ähm"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.skill != "transcript" {
		t.Fatalf("skill = %q, want transcript", cfg.skill)
	}
	if cfg.prompt != "Clean up this transcript." {
		t.Fatalf("prompt = %q", cfg.prompt)
	}
	if cfg.input != "Hello world ähm" {
		t.Fatalf("input = %q", cfg.input)
	}
}

func TestParseRunFlagsRejectsSkillAndPromptTogether(t *testing.T) {
	clearMonetlisaEnv(t)
	monetlisaHome := t.TempDir()
	skillsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillsDir, "transcript.md"), []byte("Clean up this transcript."), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := parseRunFlags([]string{"--monetlisa-home", monetlisaHome, "--skills-dir", skillsDir, "--transcript", "--prompt", "Custom prompt", "Hello"})
	if err == nil || !strings.Contains(err.Error(), "either a skill flag or -prompt") {
		t.Fatalf("err = %v, want skill/prompt conflict", err)
	}
}

func TestPostJSONReadsSSEWithoutContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var out bytes.Buffer
	err := postJSON(t.Context(), server.URL, map[string]string{"Content-Type": "application/json"}, map[string]any{"stream": true}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "done\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReadSSEExtractsResponsesAndChatDeltas(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		``,
		`data: {"choices":[{"delta":{"content":" world"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := readSSE(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Hello world\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestExtractFinalText(t *testing.T) {
	got := extractFinalText(map[string]any{
		"output": []any{
			map[string]any{
				"content": []any{
					map[string]any{"text": "clean transcript"},
				},
			},
		},
	})
	if got != "clean transcript" {
		t.Fatalf("got %q", got)
	}
}
