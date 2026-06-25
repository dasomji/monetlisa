package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	clientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	authBaseURL           = "https://auth.openai.com"
	deviceUserCodeURL     = authBaseURL + "/api/accounts/deviceauth/usercode"
	deviceTokenURL        = authBaseURL + "/api/accounts/deviceauth/token"
	deviceVerificationURI = authBaseURL + "/codex/device"
	deviceRedirectURI     = authBaseURL + "/deviceauth/callback"
	tokenURL              = authBaseURL + "/oauth/token"
	codexBaseURL          = "https://chatgpt.com/backend-api"
	jwtClaimPath          = "https://api.openai.com/auth"
	defaultPrompt         = "You are a concise, helpful assistant. Respond directly to the user's request."
)

type config struct {
	model             string
	codexHome         string
	monetlisaHome     string
	skillsDir         string
	skill             string
	prompt            string
	outputPath        string
	defaultOutputPath string
	size              string
	quality           string
	format            string
	background        string
	reasoningEffort   string
	maxOutputTokens   int
	timeout           time.Duration
	fast              bool
	input             string
}

type userSettings struct {
	Model             *string `json:"model,omitempty"`
	CodexHome         *string `json:"codex_home,omitempty"`
	SkillsDir         *string `json:"skills_dir,omitempty"`
	Prompt            *string `json:"prompt,omitempty"`
	DefaultOutputPath *string `json:"default_output_path,omitempty"`
	Size              *string `json:"size,omitempty"`
	Quality           *string `json:"quality,omitempty"`
	Format            *string `json:"format,omitempty"`
	Background        *string `json:"background,omitempty"`
	ReasoningEffort   *string `json:"reasoning_effort,omitempty"`
	MaxOutputTokens   *int    `json:"max_output_tokens,omitempty"`
	Timeout           *string `json:"timeout,omitempty"`
	Fast              *bool   `json:"fast,omitempty"`
}

type authFile struct {
	Tokens      tokenData `json:"tokens"`
	LastRefresh string    `json:"last_refresh,omitempty"`
}

type tokenData struct {
	IDToken      any    `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "monetlisa:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 && stdinIsTerminal() {
		cfg, skills, err := statusConfig(args)
		if err != nil {
			return err
		}
		printStatus(os.Stdout, cfg, skills)
		fmt.Fprintln(os.Stdout, "\nPass text as args/stdin, or run `monetlisa --help`.")
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return loginCommand(args[1:])
		case "logout":
			return logoutCommand(args[1:])
		case "auth":
			return authCommand(args[1:])
		case "status":
			cfg, skills, err := statusConfig(args[1:])
			if err != nil {
				return err
			}
			printStatus(os.Stdout, cfg, skills)
			return nil
		case "config":
			return configCommand(args[1:])
		case "help", "-h", "--help":
			printUsage()
			return nil
		}
	}

	cfg, err := parseRunFlags(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	return enhance(ctx, cfg, os.Stdout)
}

func parseRunFlags(args []string) (config, error) {
	cfg, err := initialConfig(args)
	if err != nil {
		return cfg, err
	}

	fs := flag.NewFlagSet("monetlisa", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.model, "model", cfg.model, "model name")
	fs.StringVar(&cfg.codexHome, "codex-home", cfg.codexHome, "Codex home containing auth.json")
	fs.StringVar(&cfg.monetlisaHome, "monetlisa-home", cfg.monetlisaHome, "Monetlisa home containing auth.json")
	fs.StringVar(&cfg.skillsDir, "skills-dir", cfg.skillsDir, "directory of skill markdown prompts")
	fs.StringVar(&cfg.prompt, "prompt", cfg.prompt, "system prompt")
	fs.StringVar(&cfg.outputPath, "output", cfg.outputPath, "output image path (relative paths resolve from current directory)")
	fs.StringVar(&cfg.outputPath, "o", cfg.outputPath, "output image path (shorthand)")
	fs.StringVar(&cfg.defaultOutputPath, "default-output-path", cfg.defaultOutputPath, "default output image path when -output is omitted")
	fs.StringVar(&cfg.size, "size", cfg.size, "image size: auto, 1024x1024, 1024x1536, 1536x1024")
	fs.StringVar(&cfg.quality, "quality", cfg.quality, "image quality: auto, low, medium, high")
	fs.StringVar(&cfg.format, "format", cfg.format, "image format: webp, png, jpeg")
	fs.StringVar(&cfg.background, "background", cfg.background, "background: auto or opaque")
	fs.StringVar(&cfg.reasoningEffort, "reasoning", cfg.reasoningEffort, "reasoning effort for Codex")
	fs.IntVar(&cfg.maxOutputTokens, "max-output-tokens", 0, "optional max output tokens")
	fs.DurationVar(&cfg.timeout, "timeout", cfg.timeout, "request timeout")
	fs.BoolVar(&cfg.fast, "fast", cfg.fast, "request Fast mode via Codex priority service tier")

	skills, err := discoverSkills(cfg.skillsDir)
	if err != nil {
		return cfg, err
	}
	skillFlags := make(map[string]*bool, len(skills))
	for _, skill := range skills {
		if fs.Lookup(skill.Name) != nil {
			continue
		}
		skillFlags[skill.Name] = fs.Bool(skill.Name, false, "use "+skill.Name+" skill prompt")
	}

	if err := fs.Parse(args); err != nil {
		printUsage()
		return cfg, err
	}
	if isOffValue(cfg.reasoningEffort) {
		cfg.reasoningEffort = ""
	}

	explicitPrompt := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prompt" {
			explicitPrompt = true
		}
	})
	var selectedSkills []skillPrompt
	for _, skill := range skills {
		selected, ok := skillFlags[skill.Name]
		if ok && *selected {
			selectedSkills = append(selectedSkills, skill)
		}
	}
	if len(selectedSkills) > 1 {
		return cfg, fmt.Errorf("choose only one skill prompt, got %s", joinSkillNames(selectedSkills))
	}
	if len(selectedSkills) == 1 {
		if explicitPrompt {
			return cfg, errors.New("use either a skill flag or -prompt, not both")
		}
		prompt, err := loadSkillPrompt(selectedSkills[0])
		if err != nil {
			return cfg, err
		}
		cfg.skill = selectedSkills[0].Name
		cfg.prompt = prompt
	}

	cfg.input = strings.TrimSpace(strings.Join(fs.Args(), " "))
	if cfg.input == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return cfg, err
		}
		cfg.input = strings.TrimSpace(string(data))
	}
	if cfg.input == "" {
		return cfg, errors.New("provide input text as args or stdin")
	}
	if err := validateImageSettings(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func initialConfig(args []string) (config, error) {
	home, _ := os.UserHomeDir()
	monetlisaHome := preFlagString(args, "monetlisa-home", envDefault("MONETLISA_HOME", filepath.Join(home, ".monetlisa")))
	cfg := config{
		model:         "gpt-5.5",
		codexHome:     filepath.Join(home, ".codex"),
		monetlisaHome: monetlisaHome,
		skillsDir:     filepath.Join(monetlisaHome, "skills"),
		prompt:        defaultPrompt,
		size:          "auto",
		quality:       "auto",
		format:        "webp",
		background:    "auto",
		timeout:       10 * time.Minute,
		fast:          true,
	}
	settings, err := loadSettings(settingsPath(monetlisaHome))
	if err != nil {
		return cfg, err
	}
	if err := applySettings(&cfg, settings); err != nil {
		return cfg, err
	}
	applyEnv(&cfg)
	if isOffValue(cfg.reasoningEffort) {
		cfg.reasoningEffort = ""
	}
	cfg.monetlisaHome = preFlagString(args, "monetlisa-home", cfg.monetlisaHome)
	cfg.skillsDir = preFlagString(args, "skills-dir", cfg.skillsDir)
	return cfg, nil
}

func validateImageSettings(cfg config) error {
	if !oneOf(cfg.quality, "auto", "low", "medium", "high") {
		return fmt.Errorf("quality must be auto, low, medium, or high")
	}
	if !oneOf(cfg.format, "webp", "png", "jpeg") {
		return fmt.Errorf("format must be webp, png, or jpeg")
	}
	if !oneOf(cfg.size, "auto", "1024x1024", "1024x1536", "1536x1024") {
		return fmt.Errorf("size must be auto, 1024x1024, 1024x1536, or 1536x1024")
	}
	if strings.EqualFold(cfg.background, "transparent") {
		return fmt.Errorf("background transparent is not supported by the current subscription image model; use auto or opaque")
	}
	if !oneOf(cfg.background, "auto", "opaque") {
		return fmt.Errorf("background must be auto or opaque")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if strings.EqualFold(value, item) {
			return true
		}
	}
	return false
}

func statusConfig(args []string) (config, []skillPrompt, error) {
	cfg, err := initialConfig(args)
	if err != nil {
		return cfg, nil, err
	}
	skills, err := discoverSkills(cfg.skillsDir)
	if err != nil {
		return cfg, nil, err
	}
	return cfg, skills, nil
}

func loadSettings(path string) (userSettings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return userSettings{}, nil
	}
	if err != nil {
		return userSettings{}, fmt.Errorf("read config: %w", err)
	}
	var settings userSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return userSettings{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return settings, nil
}

func saveSettings(path string, settings userSettings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func applySettings(cfg *config, settings userSettings) error {
	if settings.Model != nil {
		cfg.model = *settings.Model
	}
	if settings.CodexHome != nil {
		cfg.codexHome = *settings.CodexHome
	}
	if settings.SkillsDir != nil {
		cfg.skillsDir = *settings.SkillsDir
	}
	if settings.Prompt != nil {
		cfg.prompt = *settings.Prompt
	}
	if settings.DefaultOutputPath != nil {
		cfg.defaultOutputPath = *settings.DefaultOutputPath
	}
	if settings.Size != nil {
		cfg.size = *settings.Size
	}
	if settings.Quality != nil {
		cfg.quality = *settings.Quality
	}
	if settings.Format != nil {
		cfg.format = *settings.Format
	}
	if settings.Background != nil {
		cfg.background = *settings.Background
	}
	if settings.ReasoningEffort != nil {
		cfg.reasoningEffort = *settings.ReasoningEffort
	}
	if settings.MaxOutputTokens != nil {
		cfg.maxOutputTokens = *settings.MaxOutputTokens
	}
	if settings.Timeout != nil {
		timeout, err := time.ParseDuration(*settings.Timeout)
		if err != nil {
			return fmt.Errorf("invalid config timeout %q: %w", *settings.Timeout, err)
		}
		cfg.timeout = timeout
	}
	if settings.Fast != nil {
		cfg.fast = *settings.Fast
	}
	return nil
}

func applyEnv(cfg *config) {
	cfg.model = envDefault("MONETLISA_MODEL", cfg.model)
	cfg.codexHome = envDefault("CODEX_HOME", cfg.codexHome)
	cfg.monetlisaHome = envDefault("MONETLISA_HOME", cfg.monetlisaHome)
	cfg.skillsDir = envDefault("MONETLISA_SKILLS_DIR", cfg.skillsDir)
	cfg.prompt = envDefault("MONETLISA_PROMPT", cfg.prompt)
	cfg.defaultOutputPath = envDefault("MONETLISA_DEFAULT_OUTPUT_PATH", cfg.defaultOutputPath)
	cfg.size = envDefault("MONETLISA_SIZE", cfg.size)
	cfg.quality = envDefault("MONETLISA_QUALITY", cfg.quality)
	cfg.format = envDefault("MONETLISA_FORMAT", cfg.format)
	cfg.background = envDefault("MONETLISA_BACKGROUND", cfg.background)
	cfg.reasoningEffort = envDefault("MONETLISA_REASONING_EFFORT", cfg.reasoningEffort)
}

func settingsPath(monetlisaHome string) string {
	return filepath.Join(monetlisaHome, "config.json")
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

type skillPrompt struct {
	Name string
	Path string
}

func discoverSkills(dir string) ([]skillPrompt, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	var skills []skillPrompt
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		if strings.HasPrefix(filename, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(filename))
		if ext != ".md" && ext != ".markdown" {
			continue
		}
		name := strings.TrimSuffix(filename, filepath.Ext(filename))
		if !validSkillFlagName(name) {
			continue
		}
		skills = append(skills, skillPrompt{
			Name: name,
			Path: filepath.Join(dir, filename),
		})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

func validSkillFlagName(name string) bool {
	return name != "" && !strings.HasPrefix(name, "-") && !strings.ContainsAny(name, "= \t\r\n")
}

func loadSkillPrompt(skill skillPrompt) (string, error) {
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", skill.Name, err)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("skill %q is empty", skill.Name)
	}
	return prompt, nil
}

func joinSkillNames(skills []skillPrompt) string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, "--"+skill.Name)
	}
	return strings.Join(names, ", ")
}

func preFlagString(args []string, name, fallback string) string {
	long := "--" + name
	short := "-" + name
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, long+"=") {
			return strings.TrimPrefix(arg, long+"=")
		}
		if strings.HasPrefix(arg, short+"=") {
			return strings.TrimPrefix(arg, short+"=")
		}
		if arg == long || arg == short {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return fallback
}

func configCommand(args []string) error {
	home, _ := os.UserHomeDir()
	defaultHome := envDefault("MONETLISA_HOME", filepath.Join(home, ".monetlisa"))
	monetlisaHome := preFlagString(args, "monetlisa-home", defaultHome)
	path := settingsPath(monetlisaHome)
	settings, err := loadSettings(path)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&monetlisaHome, "monetlisa-home", monetlisaHome, "Monetlisa home containing config.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "show" {
		cfg, skills, err := statusConfig([]string{"--monetlisa-home", monetlisaHome})
		if err != nil {
			return err
		}
		printStatus(os.Stdout, cfg, skills)
		return nil
	}
	if rest[0] == "path" {
		fmt.Fprintln(os.Stdout, path)
		return nil
	}
	if rest[0] == "set" {
		if len(rest) < 3 {
			return errors.New("usage: monetlisa config set <key> <value>")
		}
		if err := setSetting(&settings, rest[1], strings.Join(rest[2:], " ")); err != nil {
			return err
		}
		if err := saveSettings(path, settings); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Saved %s\n", path)
		return nil
	}
	if rest[0] == "unset" {
		if len(rest) != 2 {
			return errors.New("usage: monetlisa config unset <key>")
		}
		if err := unsetSetting(&settings, rest[1]); err != nil {
			return err
		}
		if err := saveSettings(path, settings); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Saved %s\n", path)
		return nil
	}
	return fmt.Errorf("unknown config command %q", rest[0])
}

func setSetting(settings *userSettings, key, value string) error {
	key = normalizeSettingKey(key)
	s := value
	switch key {
	case "model":
		settings.Model = &s
	case "codex-home":
		settings.CodexHome = &s
	case "skills-dir":
		settings.SkillsDir = &s
	case "prompt":
		settings.Prompt = &s
	case "default-output-path":
		settings.DefaultOutputPath = &s
	case "size":
		settings.Size = &s
	case "quality":
		settings.Quality = &s
	case "format":
		settings.Format = &s
	case "background":
		settings.Background = &s
	case "reasoning":
		if isOffValue(value) {
			s = ""
		}
		settings.ReasoningEffort = &s
	case "max-output-tokens":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("max-output-tokens must be a non-negative integer")
		}
		settings.MaxOutputTokens = &n
	case "timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("timeout must be a duration like 30s or 10m: %w", err)
		}
		settings.Timeout = &s
	case "fast":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("fast must be true or false")
		}
		settings.Fast = &b
	default:
		return unknownSettingError(key)
	}
	return nil
}

func unsetSetting(settings *userSettings, key string) error {
	switch normalizeSettingKey(key) {
	case "model":
		settings.Model = nil
	case "codex-home":
		settings.CodexHome = nil
	case "skills-dir":
		settings.SkillsDir = nil
	case "prompt":
		settings.Prompt = nil
	case "default-output-path":
		settings.DefaultOutputPath = nil
	case "size":
		settings.Size = nil
	case "quality":
		settings.Quality = nil
	case "format":
		settings.Format = nil
	case "background":
		settings.Background = nil
	case "reasoning":
		settings.ReasoningEffort = nil
	case "max-output-tokens":
		settings.MaxOutputTokens = nil
	case "timeout":
		settings.Timeout = nil
	case "fast":
		settings.Fast = nil
	default:
		return unknownSettingError(key)
	}
	return nil
}

func normalizeSettingKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "_", "-")
	if key == "reasoning-effort" {
		return "reasoning"
	}
	return key
}

func isOffValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "off", "false", "no", "default":
		return true
	default:
		return false
	}
}

func unknownSettingError(key string) error {
	return fmt.Errorf("unknown setting %q (use model, codex-home, skills-dir, prompt, default-output-path, size, quality, format, background, reasoning, max-output-tokens, timeout, or fast)", key)
}

func printStatus(out io.Writer, cfg config, skills []skillPrompt) {
	fmt.Fprintln(out, "monetlisa defaults")
	fmt.Fprintf(out, "config: %s\n", settingsPath(cfg.monetlisaHome))
	fmt.Fprintf(out, "model: %s\n", cfg.model)
	fmt.Fprintf(out, "reasoning: %s\n", displayValue(cfg.reasoningEffort, "off"))
	fmt.Fprintf(out, "fast: %t\n", cfg.fast)
	fmt.Fprintf(out, "timeout: %s\n", cfg.timeout)
	fmt.Fprintf(out, "output_path: %s\n", displayValue(effectiveOutputPath(cfg), "auto in current directory"))
	fmt.Fprintf(out, "size: %s\n", cfg.size)
	fmt.Fprintf(out, "quality: %s\n", cfg.quality)
	fmt.Fprintf(out, "format: %s\n", cfg.format)
	fmt.Fprintf(out, "background: %s\n", cfg.background)
	fmt.Fprintf(out, "max_output_tokens: %s\n", displayInt(cfg.maxOutputTokens, "unlimited"))
	fmt.Fprintf(out, "skills_dir: %s\n", cfg.skillsDir)
	fmt.Fprintf(out, "skill_count: %d\n", len(skills))
	fmt.Fprintf(out, "prompt: %s\n", oneLine(cfg.prompt, 96))
}

func displayValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func displayInt(value int, fallback string) string {
	if value == 0 {
		return fallback
	}
	return strconv.Itoa(value)
}

func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit-1] + "…"
}

func enhance(ctx context.Context, cfg config, out io.Writer) error {
	return callCodex(ctx, cfg, out)
}

func callCodex(ctx context.Context, cfg config, out io.Writer) error {
	auth, path, err := loadCodexLikeAuth(cfg.monetlisaHome, cfg.codexHome)
	if err != nil {
		return err
	}
	if shouldRefresh(auth.Tokens.AccessToken) {
		if auth.Tokens.RefreshToken == "" {
			return errors.New("Codex access token is expired and no refresh token is available; run `monetlisa login` or `codex login`")
		}
		refreshed, err := refreshToken(ctx, auth.Tokens.RefreshToken)
		if err != nil {
			return err
		}
		auth.Tokens.AccessToken = refreshed.AccessToken
		auth.Tokens.RefreshToken = refreshed.RefreshToken
		auth.Tokens.AccountID = accountIDFromToken(refreshed.AccessToken)
		auth.LastRefresh = time.Now().UTC().Format(time.RFC3339)
		if strings.Contains(path, string(filepath.Separator)+".monetlisa"+string(filepath.Separator)) {
			_ = saveAuth(path, auth)
		}
	}

	accountID := auth.Tokens.AccountID
	if accountID == "" {
		accountID = accountIDFromToken(auth.Tokens.AccessToken)
	}
	if accountID == "" {
		accountID = accountIDFromIDToken(auth.Tokens.IDToken)
	}
	if accountID == "" {
		return errors.New("could not find chatgpt account id in Codex auth")
	}

	endpoint := codexBaseURL + "/codex/responses"
	body := codexRequestBody(cfg)

	headers := map[string]string{
		"Authorization":       "Bearer " + auth.Tokens.AccessToken,
		"chatgpt-account-id":  accountID,
		"originator":          "monetlisa",
		"OpenAI-Beta":         "responses=experimental",
		"Accept":              "text/event-stream",
		"Content-Type":        "application/json",
		"User-Agent":          "monetlisa/0.1",
		"X-Client-Request-Id": randomID(),
	}
	return postImageJSON(ctx, endpoint, headers, body, cfg, out)
}

func codexRequestBody(cfg config) map[string]any {
	body := map[string]any{
		"model":        cfg.model,
		"instructions": cfg.prompt,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]string{
					{"type": "input_text", "text": cfg.input},
				},
			},
		},
		"tools":  []map[string]any{imageGenerationTool(cfg)},
		"store":  false,
		"stream": true,
	}
	if cfg.fast {
		body["service_tier"] = "priority"
	}
	addResponsesOptions(body, cfg)
	return body
}

func imageGenerationTool(cfg config) map[string]any {
	tool := map[string]any{"type": "image_generation"}
	if cfg.size != "" {
		tool["size"] = cfg.size
	}
	if cfg.quality != "" {
		tool["quality"] = cfg.quality
	}
	if cfg.format != "" {
		tool["output_format"] = cfg.format
	}
	if cfg.background != "" {
		tool["background"] = cfg.background
	}
	return tool
}

func addResponsesOptions(body map[string]any, cfg config) {
	if cfg.reasoningEffort != "" {
		body["reasoning"] = map[string]string{"effort": cfg.reasoningEffort}
	}
	if cfg.maxOutputTokens > 0 {
		body["max_output_tokens"] = cfg.maxOutputTokens
	}
}

func postImageJSON(ctx context.Context, endpoint string, headers map[string]string, body map[string]any, cfg config, out io.Writer) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	image, err := extractImageBase64(response)
	if err != nil {
		return err
	}
	path := effectiveOutputPath(cfg)
	if path == "" {
		path = defaultGeneratedOutputPath(cfg)
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		path = filepath.Join(cwd, path)
	}
	if err := writeBase64Image(path, image); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", path)
	return nil
}

func effectiveOutputPath(cfg config) string {
	if strings.TrimSpace(cfg.outputPath) != "" {
		return strings.TrimSpace(cfg.outputPath)
	}
	return strings.TrimSpace(cfg.defaultOutputPath)
}

func defaultGeneratedOutputPath(cfg config) string {
	ext := strings.ToLower(strings.TrimPrefix(cfg.format, "."))
	if ext == "" || ext == "auto" {
		ext = "webp"
	}
	return fmt.Sprintf("monetlisa-%s.%s", time.Now().Format("20060102-150405"), ext)
}

func writeBase64Image(path, image string) error {
	data, err := base64.StdEncoding.DecodeString(image)
	if err != nil {
		return fmt.Errorf("decode generated image: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func extractImageBase64(data []byte) (string, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", errors.New("image response was empty")
	}
	var obj map[string]any
	if json.Unmarshal(data, &obj) == nil {
		if image := imageBase64FromObject(obj); image != "" {
			return image, nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "event:") || line == "data: [DONE]" {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil {
			if image := imageBase64FromObject(event); image != "" {
				return image, nil
			}
		}
	}
	return "", errors.New("response did not include an image_generation_call result")
}

func imageBase64FromObject(obj map[string]any) string {
	if result, ok := obj["result"].(string); ok && result != "" {
		return result
	}
	if item, ok := obj["item"].(map[string]any); ok {
		if result, ok := item["result"].(string); ok && result != "" && item["type"] == "image_generation_call" {
			return result
		}
	}
	if output, ok := obj["output"].([]any); ok {
		for _, item := range output {
			if m, ok := item.(map[string]any); ok {
				if result, ok := m["result"].(string); ok && result != "" && m["type"] == "image_generation_call" {
					return result
				}
			}
		}
	}
	return ""
}

func postJSON(ctx context.Context, endpoint string, headers map[string]string, body map[string]any, out io.Writer) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	if isJSON(resp.Header.Get("Content-Type")) {
		return readJSONResponse(resp.Body, out)
	}
	return readSSE(resp.Body, out)
}

func readSSE(r io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var event strings.Builder
	wrote := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if text := textFromSSEEvent(event.String()); text != "" {
				_, _ = io.WriteString(out, text)
				wrote = true
			}
			event.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			event.WriteString(data)
			event.WriteByte('\n')
		}
	}
	if event.Len() > 0 {
		if text := textFromSSEEvent(event.String()); text != "" {
			_, _ = io.WriteString(out, text)
			wrote = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if wrote {
		_, _ = io.WriteString(out, "\n")
	}
	return nil
}

func textFromSSEEvent(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return ""
	}
	if choices, ok := obj["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				if content, ok := delta["content"].(string); ok {
					return content
				}
			}
		}
	}
	if delta, ok := obj["delta"].(string); ok {
		return delta
	}
	if text, ok := obj["text"].(string); ok && strings.Contains(fmt.Sprint(obj["type"]), "delta") {
		return text
	}
	return ""
}

func readJSONResponse(r io.Reader, out io.Writer) error {
	var obj map[string]any
	if err := json.NewDecoder(r).Decode(&obj); err != nil {
		return err
	}
	text := extractFinalText(obj)
	if text == "" {
		pretty, _ := json.MarshalIndent(obj, "", "  ")
		_, _ = out.Write(pretty)
		_, _ = io.WriteString(out, "\n")
		return nil
	}
	_, _ = io.WriteString(out, text)
	if !strings.HasSuffix(text, "\n") {
		_, _ = io.WriteString(out, "\n")
	}
	return nil
}

func extractFinalText(obj map[string]any) string {
	if text, ok := obj["output_text"].(string); ok {
		return text
	}
	if choices, ok := obj["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				if content, ok := msg["content"].(string); ok {
					return content
				}
			}
		}
	}
	var b strings.Builder
	if output, ok := obj["output"].([]any); ok {
		for _, item := range output {
			m, _ := item.(map[string]any)
			content, _ := m["content"].([]any)
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if text, ok := cm["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
	}
	return b.String()
}

func loginCommand(args []string) error {
	home, _ := os.UserHomeDir()
	monetlisaHome := envDefault("MONETLISA_HOME", filepath.Join(home, ".monetlisa"))
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&monetlisaHome, "monetlisa-home", monetlisaHome, "Monetlisa home for auth.json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	device, err := startDeviceAuth(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Open %s and enter code: %s\n", deviceVerificationURI, device.UserCode)
	code, verifier, err := pollDeviceAuth(ctx, device)
	if err != nil {
		return err
	}
	tok, err := exchangeDeviceCode(ctx, code, verifier)
	if err != nil {
		return err
	}
	auth := authFile{
		Tokens: tokenData{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			AccountID:    accountIDFromToken(tok.AccessToken),
		},
		LastRefresh: time.Now().UTC().Format(time.RFC3339),
	}
	if auth.Tokens.AccountID == "" {
		return errors.New("login succeeded but access token did not include account id")
	}
	path := filepath.Join(monetlisaHome, "auth.json")
	if err := saveAuth(path, auth); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Saved Codex subscription auth to %s\n", path)
	return nil
}

func logoutCommand(args []string) error {
	home, _ := os.UserHomeDir()
	monetlisaHome := envDefault("MONETLISA_HOME", filepath.Join(home, ".monetlisa"))
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&monetlisaHome, "monetlisa-home", monetlisaHome, "Monetlisa home for auth.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := filepath.Join(monetlisaHome, "auth.json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(os.Stderr, "Removed %s\n", path)
	return nil
}

func authCommand(args []string) error {
	home, _ := os.UserHomeDir()
	monetlisaHome := envDefault("MONETLISA_HOME", filepath.Join(home, ".monetlisa"))
	codexHome := envDefault("CODEX_HOME", filepath.Join(home, ".codex"))
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&monetlisaHome, "monetlisa-home", monetlisaHome, "Monetlisa home containing auth.json")
	fs.StringVar(&codexHome, "codex-home", codexHome, "Codex home containing auth.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	auth, path, err := loadCodexLikeAuth(monetlisaHome, codexHome)
	if err != nil {
		return err
	}
	accountID := auth.Tokens.AccountID
	if accountID == "" {
		accountID = accountIDFromToken(auth.Tokens.AccessToken)
	}
	fmt.Printf("auth_file=%s\naccount_id=%s\naccess_token_expired=%t\n", path, accountID, shouldRefresh(auth.Tokens.AccessToken))
	return nil
}

type deviceAuth struct {
	DeviceAuthID   string
	UserCode       string
	IntervalSecond int
}

func startDeviceAuth(ctx context.Context) (deviceAuth, error) {
	reqBody := strings.NewReader(`{"client_id":"` + clientID + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceUserCodeURL, reqBody)
	if err != nil {
		return deviceAuth{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return deviceAuth{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return deviceAuth{}, responseError(resp)
	}
	var raw struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     any    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return deviceAuth{}, err
	}
	interval := 5
	switch v := raw.Interval.(type) {
	case float64:
		interval = int(v)
	case string:
		fmt.Sscanf(v, "%d", &interval)
	}
	if raw.DeviceAuthID == "" || raw.UserCode == "" {
		return deviceAuth{}, errors.New("invalid device auth response")
	}
	return deviceAuth{DeviceAuthID: raw.DeviceAuthID, UserCode: raw.UserCode, IntervalSecond: interval}, nil
}

func pollDeviceAuth(ctx context.Context, d deviceAuth) (authorizationCode string, codeVerifier string, err error) {
	interval := time.Duration(d.IntervalSecond) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-timer.C:
		}

		body := fmt.Sprintf(`{"device_auth_id":%q,"user_code":%q}`, d.DeviceAuthID, d.UserCode)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceTokenURL, strings.NewReader(body))
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", "", err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			var ok struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.Unmarshal(raw, &ok); err != nil {
				return "", "", err
			}
			if ok.AuthorizationCode == "" || ok.CodeVerifier == "" {
				return "", "", errors.New("invalid device token response")
			}
			return ok.AuthorizationCode, ok.CodeVerifier, nil
		}
		if resp.StatusCode == 403 || resp.StatusCode == 404 || bytes.Contains(raw, []byte("authorization_pending")) {
			timer.Reset(interval)
			continue
		}
		if bytes.Contains(raw, []byte("slow_down")) {
			interval += 5 * time.Second
			timer.Reset(interval)
			continue
		}
		return "", "", fmt.Errorf("device auth failed: %s", strings.TrimSpace(string(raw)))
	}
}

func exchangeDeviceCode(ctx context.Context, code, verifier string) (oauthTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {deviceRedirectURI},
	}
	return requestToken(ctx, form)
}

func refreshToken(ctx context.Context, refresh string) (oauthTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refresh},
	}
	return requestToken(ctx, form)
}

func requestToken(ctx context.Context, form url.Values) (oauthTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthTokenResponse{}, responseError(resp)
	}
	var tok oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return oauthTokenResponse{}, err
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return oauthTokenResponse{}, errors.New("token response missing access_token or refresh_token")
	}
	return tok, nil
}

func loadCodexLikeAuth(monetlisaHome, codexHome string) (authFile, string, error) {
	paths := []string{
		filepath.Join(monetlisaHome, "auth.json"),
		filepath.Join(codexHome, "auth.json"),
	}
	var lastErr error
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}
		var auth authFile
		if err := json.Unmarshal(data, &auth); err != nil {
			return authFile{}, path, err
		}
		if auth.Tokens.AccessToken != "" {
			return auth, path, nil
		}
		lastErr = fmt.Errorf("%s did not contain Codex tokens", path)
	}
	return authFile{}, "", fmt.Errorf("no Codex subscription auth found; run `monetlisa login` or `codex login` first (%v)", lastErr)
}

func saveAuth(path string, auth authFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func shouldRefresh(jwt string) bool {
	exp := jwtExp(jwt)
	if exp.IsZero() {
		return false
	}
	return time.Until(exp) < 2*time.Minute
}

func jwtExp(jwt string) time.Time {
	var payload map[string]any
	if !decodeJWTPayload(jwt, &payload) {
		return time.Time{}
	}
	exp, ok := payload["exp"].(float64)
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0)
}

func accountIDFromToken(jwt string) string {
	var payload map[string]any
	if !decodeJWTPayload(jwt, &payload) {
		return ""
	}
	if auth, ok := payload[jwtClaimPath].(map[string]any); ok {
		if accountID, ok := auth["chatgpt_account_id"].(string); ok {
			return accountID
		}
	}
	return ""
}

func accountIDFromIDToken(value any) string {
	switch v := value.(type) {
	case string:
		return accountIDFromToken(v)
	case map[string]any:
		if accountID, ok := v["chatgpt_account_id"].(string); ok {
			return accountID
		}
	}
	return ""
}

func decodeJWTPayload(token string, dst any) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	return json.Unmarshal(data, dst) == nil
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("HTTP %s: %s", resp.Status, msg)
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("monetlisa-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func printUsage() {
	cfg, _, err := statusConfig(nil)
	if err != nil {
		cfg = config{model: "gpt-5.5", prompt: defaultPrompt, size: "auto", quality: "auto", format: "webp", background: "auto", timeout: 10 * time.Minute, fast: true}
	}
	fmt.Fprintf(os.Stderr, `monetlisa - terminal image generation with OpenAI subscription auth

Usage:
  monetlisa [flags] "image prompt"
  monetlisa -o cover.png "editorial cover art of a robot gardener"
  cat prompt.txt | monetlisa --output images/generated.png
  monetlisa status
  monetlisa config set default-output-path images/latest.png
  monetlisa login
  monetlisa auth
  monetlisa logout

Defaults to Codex subscription auth. Run monetlisa login or codex login first.
Relative output paths resolve from the current working directory.

Current defaults:
  model: %s
  output_path: %s
  size: %s
  quality: %s
  format: %s
  background: %s
  fast: %t

Flags:
  -model %s
  -o PATH, -output PATH
  -default-output-path PATH
  -size %s
  -quality %s
  -format %s
  -background %s
  -prompt TEXT
  -skills-dir DIR
  -fast=%t
  -timeout %s
  -codex-home DIR
  -monetlisa-home DIR
`, cfg.model, displayValue(effectiveOutputPath(cfg), "auto in current directory"), cfg.size, cfg.quality, cfg.format, cfg.background, cfg.fast, cfg.model, cfg.size, cfg.quality, cfg.format, cfg.background, cfg.fast, cfg.timeout)
}
