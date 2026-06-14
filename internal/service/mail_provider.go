package service

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/util"
)

var (
	registerMailDomainMu    sync.Mutex
	registerMailProviderMu  sync.Mutex
	registerMailDomainSeq   int
	registerMailProviderSeq int

	registerMailCodePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)background-color:\s*#F3F3F3[^>]*>[\s\S]*?(\d{6})[\s\S]*?</p>`),
		regexp.MustCompile(`(?i)(?:Verification code|code is|代码为|验证码)[:\s]*(\d{6})`),
		regexp.MustCompile(`(?is)>\s*(\d{6})\s*<`),
		regexp.MustCompile(`\b(\d{6})\b`),
	}
)

type registerMailboxProvider interface {
	CreateMailbox(username string) (map[string]any, error)
	FetchLatestMessage(mailbox map[string]any) (map[string]any, error)
	Close()
}

type registerMailSettings struct {
	RequestTimeout time.Duration
	WaitTimeout    time.Duration
	WaitInterval   time.Duration
	UserAgent      string
}

type registerHTTPMailProvider struct {
	client *http.Client
	conf   registerMailSettings
}

type registerCloudflareTempMailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerTempMailLOLProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerDuckMailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerGPTMailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerMoEmailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerInbucketMailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerYYDSMailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

type registerHLOOLMailProvider struct {
	registerHTTPMailProvider
	entry map[string]any
}

func createRegisterMailbox(mailConfig map[string]any, proxy, username string) (map[string]any, error) {
	provider, err := createRegisterMailProvider(mailConfig, proxy, "", "")
	if err != nil {
		return nil, err
	}
	defer provider.Close()
	return provider.CreateMailbox(username)
}

func waitRegisterCode(ctx context.Context, mailConfig map[string]any, proxy string, mailbox map[string]any) (string, error) {
	provider, err := createRegisterMailProvider(mailConfig, proxy, util.Clean(mailbox["provider"]), util.Clean(mailbox["provider_ref"]))
	if err != nil {
		return "", err
	}
	defer provider.Close()
	conf := registerMailSettingsFromConfig(mailConfig)
	deadline := time.NewTimer(conf.WaitTimeout)
	defer deadline.Stop()
	for {
		message, fetchErr := provider.FetchLatestMessage(mailbox)
		if fetchErr == nil && message != nil {
			if code := extractUnseenRegisterMailCode(mailbox, message); code != "" {
				return code, nil
			}
		}
		interval := time.NewTimer(conf.WaitInterval)
		select {
		case <-ctx.Done():
			interval.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			interval.Stop()
			return "", nil
		case <-interval.C:
		}
	}
}

func createRegisterMailProvider(mailConfig map[string]any, proxy, providerName, providerRef string) (registerMailboxProvider, error) {
	entry, err := selectRegisterMailEntry(mailConfig, providerName, providerRef)
	if err != nil {
		return nil, err
	}
	conf := registerMailSettingsFromConfig(mailConfig)
	client := registerMailHTTPClient(conf.RequestTimeout, proxy)
	base := registerHTTPMailProvider{client: client, conf: conf}
	switch util.Clean(entry["type"]) {
	case "cloudflare_temp_email":
		return &registerCloudflareTempMailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "tempmail_lol":
		return &registerTempMailLOLProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "duckmail":
		return &registerDuckMailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "gptmail":
		return &registerGPTMailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "moemail":
		return &registerMoEmailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "inbucket":
		return &registerInbucketMailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "yyds_mail":
		return &registerYYDSMailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	case "hlool_mail":
		return &registerHLOOLMailProvider{registerHTTPMailProvider: base, entry: entry}, nil
	default:
		return nil, fmt.Errorf("unsupported mail.provider: %s", util.Clean(entry["type"]))
	}
}

func registerMailSettingsFromConfig(mailConfig map[string]any) registerMailSettings {
	return registerMailSettings{
		RequestTimeout: time.Duration(maxInt(1, util.ToInt(mailConfig["request_timeout"], 30))) * time.Second,
		WaitTimeout:    time.Duration(maxInt(1, util.ToInt(mailConfig["wait_timeout"], 30))) * time.Second,
		WaitInterval:   time.Duration(maxInt(1, util.ToInt(mailConfig["wait_interval"], 3))) * time.Second,
		UserAgent:      firstNonEmpty(util.Clean(mailConfig["user_agent"]), "Mozilla/5.0"),
	}
}

func registerMailHTTPClient(timeout time.Duration, proxy string) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		},
	}
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		if proxyURL, err := url.Parse(proxy); err == nil && proxyURL.Host != "" {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func registerMailEntries(mailConfig map[string]any) []map[string]any {
	providers := util.AsMapSlice(mailConfig["providers"])
	out := make([]map[string]any, 0, len(providers))
	for index, item := range providers {
		entry := util.CopyMap(item)
		entry["provider_ref"] = fmt.Sprintf("%s#%d", util.Clean(entry["type"]), index+1)
		out = append(out, entry)
	}
	return out
}

func selectRegisterMailEntry(mailConfig map[string]any, providerName, providerRef string) (map[string]any, error) {
	entries := registerMailEntries(mailConfig)
	enabled := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if util.ToBool(entry["enable"]) {
			enabled = append(enabled, entry)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("mail.providers has no enabled provider")
	}
	if providerRef != "" {
		for _, entry := range entries {
			if util.Clean(entry["provider_ref"]) == providerRef {
				return util.CopyMap(entry), nil
			}
		}
	}
	if providerName != "" {
		for _, entry := range enabled {
			if util.Clean(entry["type"]) == providerName {
				return util.CopyMap(entry), nil
			}
		}
	}
	if len(enabled) == 1 {
		return util.CopyMap(enabled[0]), nil
	}
	registerMailProviderMu.Lock()
	entry := util.CopyMap(enabled[registerMailProviderSeq%len(enabled)])
	registerMailProviderSeq = (registerMailProviderSeq + 1) % len(enabled)
	registerMailProviderMu.Unlock()
	return entry, nil
}

func extractRegisterMailCode(message map[string]any) string {
	textContent, htmlContent := extractRegisterMailContent(message)
	content := strings.TrimSpace(strings.Join([]string{
		util.Clean(message["subject"]),
		textContent,
		htmlContent,
	}, "\n"))
	if content == "" {
		return ""
	}
	for _, pattern := range registerMailCodePatterns {
		match := pattern.FindStringSubmatch(content)
		if len(match) > 1 {
			code := strings.TrimSpace(match[1])
			if code != "" && code != "177010" {
				return code
			}
		}
	}
	return ""
}

func extractUnseenRegisterMailCode(mailbox map[string]any, message map[string]any) string {
	ref := registerMailMessageRef(message)
	seen := registerSeenMailRefs(mailbox["_seen_code_message_refs"])
	if ref != "" {
		if _, ok := seen[ref]; ok {
			return ""
		}
	}
	code := extractRegisterMailCode(message)
	if code == "" || ref == "" {
		return code
	}
	existing := registerSeenMailRefList(mailbox["_seen_code_message_refs"])
	mailbox["_seen_code_message_refs"] = append(existing, ref)
	return code
}

func registerSeenMailRefs(value any) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range registerSeenMailRefList(value) {
		out[item] = struct{}{}
	}
	return out
}

func registerSeenMailRefList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if ref := util.Clean(item); ref != "" {
				out = append(out, ref)
			}
		}
		return out
	default:
		return nil
	}
}

func registerMailMessageRef(message map[string]any) string {
	provider := util.Clean(message["provider"])
	mailbox := util.Clean(message["mailbox"])
	if id := registerMessageID(message); id != "" {
		return "id:" + provider + ":" + mailbox + ":" + id
	}
	textContent, htmlContent := extractRegisterMailContent(message)
	received := util.Clean(message["received_at"])
	content := strings.Join([]string{util.Clean(message["subject"]), textContent, htmlContent}, "\n")
	if strings.TrimSpace(content) == "" {
		return ""
	}
	sum := sha1.Sum([]byte(content))
	return fmt.Sprintf("content:%s:%s:%s:%x", provider, mailbox, received, sum[:8])
}

func extractRegisterMailContent(data map[string]any) (string, string) {
	textContent := firstNonEmpty(
		registerContentString(data["text_content"]),
		registerContentString(data["text"]),
		registerContentString(data["body"]),
		registerContentString(data["content"]),
	)
	htmlContent := firstNonEmpty(
		registerContentString(data["html_content"]),
		registerContentString(data["html"]),
		registerContentString(data["html_body"]),
		registerContentString(data["body_html"]),
	)
	if textContent != "" || htmlContent != "" {
		return textContent, htmlContent
	}
	raw, ok := data["raw"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", ""
	}
	textContent, htmlContent = parseRegisterRawMail(raw)
	if textContent == "" && htmlContent == "" {
		return raw, ""
	}
	return textContent, htmlContent
}

func registerContentString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []string:
		return strings.TrimSpace(strings.Join(typed, ""))
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := registerContentString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, ""))
	default:
		return util.Clean(value)
	}
}

func parseRegisterRawMail(raw string) (string, string) {
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return raw, ""
	}
	plain, html := parseRegisterMIMEBody(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
	return strings.TrimSpace(strings.Join(plain, "\n")), strings.TrimSpace(strings.Join(html, "\n"))
}

func parseRegisterMIMEBody(contentType, transferEncoding string, body io.Reader) ([]string, []string) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(strings.Split(contentType, ";")[0]))
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil, nil
		}
		reader := multipart.NewReader(body, boundary)
		var plain []string
		var html []string
		for {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}
			partPlain, partHTML := parseRegisterMIMEBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			plain = append(plain, partPlain...)
			html = append(html, partHTML...)
		}
		return plain, html
	}
	payload, err := readRegisterMIMEPayload(body, transferEncoding)
	if err != nil || strings.TrimSpace(payload) == "" {
		return nil, nil
	}
	if mediaType == "text/html" {
		return nil, []string{payload}
	}
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") {
		return []string{payload}, nil
	}
	return nil, nil
}

func readRegisterMIMEPayload(body io.Reader, transferEncoding string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		data, err := io.ReadAll(body)
		if err != nil {
			return "", err
		}
		cleaned := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(string(data))
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	case "quoted-printable":
		data, err := io.ReadAll(quotedprintable.NewReader(body))
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		data, err := io.ReadAll(body)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

func registerMessageMatchesEmail(data map[string]any, email string) bool {
	target := strings.ToLower(strings.TrimSpace(email))
	if target == "" {
		return true
	}
	var candidates []string
	for _, key := range []string{"to", "mailTo", "receiver", "receivers", "address", "email", "envelope_to"} {
		if value, ok := data[key]; ok {
			candidates = append(candidates, registerTextCandidates(value)...)
		}
	}
	if len(candidates) == 0 {
		return true
	}
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(strings.TrimSpace(candidate)), target) {
			return true
		}
	}
	return false
}

func registerTextCandidates(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case map[string]any:
		var out []string
		for _, key := range []string{"address", "email", "name", "value"} {
			if item, ok := typed[key]; ok {
				out = append(out, registerTextCandidates(item)...)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range typed {
			out = append(out, registerTextCandidates(item)...)
		}
		return out
	case []map[string]any:
		var out []string
		for _, item := range typed {
			out = append(out, registerTextCandidates(item)...)
		}
		return out
	default:
		return nil
	}
}

func latestRegisterMailMessage(items []map[string]any) map[string]any {
	if len(items) == 0 {
		return nil
	}
	candidates := append([]map[string]any(nil), items...)
	sort.SliceStable(candidates, func(i, j int) bool {
		left := registerMessageReceivedAt(candidates[i])
		right := registerMessageReceivedAt(candidates[j])
		if !left.IsZero() || !right.IsZero() {
			if !left.Equal(right) {
				return left.After(right)
			}
			return registerMessageID(candidates[i]) > registerMessageID(candidates[j])
		}
		return false
	})
	return candidates[0]
}

func registerMessageReceivedAt(data map[string]any) time.Time {
	for _, key := range []string{"created_at", "createdAt", "received_at", "receivedAt", "date", "timestamp"} {
		if value, ok := data[key]; ok {
			if parsed := parseRegisterMailTime(value); !parsed.IsZero() {
				return parsed
			}
		}
	}
	return time.Time{}
}

func registerMessageID(data map[string]any) string {
	return util.Clean(firstNonNil(data["id"], data["message_id"], data["_id"], data["token"], data["@id"]))
}

func parseRegisterMailTime(value any) time.Time {
	switch typed := value.(type) {
	case int:
		return time.Unix(int64(typed), 0).UTC()
	case int64:
		return time.Unix(typed, 0).UTC()
	case float64:
		return time.Unix(int64(typed), 0).UTC()
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return time.Unix(integer, 0).UTC()
		}
		if number, err := typed.Float64(); err == nil {
			return time.Unix(int64(number), 0).UTC()
		}
	}
	text := util.Clean(value)
	if text == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC1123Z, text); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC1123, text); err == nil {
		return parsed
	}
	if parsed, err := mail.ParseDate(text); err == nil {
		return parsed
	}
	return time.Time{}
}

func registerRandomMailboxName() string {
	return fmt.Sprintf("%s%d%s", randomLower(5), rand.Intn(999), randomLower(2+rand.Intn(2)))
}

func registerRandomSubdomainLabel() string {
	return randomAlphaNum(4 + rand.Intn(7))
}

func nextRegisterDomain(domains []string, blocked ...string) (string, error) {
	blockedSet := make(map[string]bool, len(blocked))
	for _, d := range blocked {
		if d = strings.TrimSpace(d); d != "" {
			blockedSet[d] = true
		}
	}
	filtered := make([]string, 0, len(domains))
	for _, domain := range domains {
		if item := strings.TrimSpace(domain); item != "" && !blockedSet[item] {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return "", fmt.Errorf("all mail domains are blocked or empty")
	}
	if len(filtered) == 1 {
		return filtered[0], nil
	}
	registerMailDomainMu.Lock()
	value := filtered[registerMailDomainSeq%len(filtered)]
	registerMailDomainSeq = (registerMailDomainSeq + 1) % len(filtered)
	registerMailDomainMu.Unlock()
	return value, nil
}

func randomLower(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(letters[rand.Intn(len(letters))])
	}
	return b.String()
}

func randomAlphaNum(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(chars[rand.Intn(len(chars))])
	}
	return b.String()
}

func (p *registerHTTPMailProvider) Close() {
	p.client.CloseIdleConnections()
}

func yydsMailItems(data any) []map[string]any {
	switch typed := data.(type) {
	case []map[string]any:
		return typed
	case []any:
		return util.AsMapSlice(typed)
	case map[string]any:
		return util.AsMapSlice(firstNonNil(typed["items"], typed["messages"], typed["data"]))
	default:
		return nil
	}
}

func duckMailItems(data any) []map[string]any {
	switch typed := data.(type) {
	case []any:
		return util.AsMapSlice(typed)
	case map[string]any:
		return util.AsMapSlice(firstNonNil(typed["hydra:member"], typed["member"], typed["data"]))
	default:
		return nil
	}
}

func registerMailRequestJSON(client *http.Client, method, target string, headers map[string]string, query map[string]string, payload any, expected ...int) (map[string]any, error) {
	data, err := registerMailRequestAny(client, method, target, headers, query, payload, expected...)
	if err != nil {
		return nil, err
	}
	return util.StringMap(data), nil
}

func registerMailRequestAny(client *http.Client, method, target string, headers map[string]string, query map[string]string, payload any, expected ...int) (any, error) {
	var bodyReader *strings.Reader
	if payload == nil {
		bodyReader = strings.NewReader("")
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(string(data))
	}
	if len(query) > 0 {
		parsed, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		values := parsed.Query()
		for key, value := range query {
			if strings.TrimSpace(value) != "" {
				values.Set(key, value)
			}
		}
		parsed.RawQuery = values.Encode()
		target = parsed.String()
	}
	req, err := http.NewRequest(method, target, bodyReader)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !registerExpectedStatus(resp.StatusCode, expected...) {
		return nil, fmt.Errorf("mail request failed: %s %s -> HTTP %d", method, target, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNoContent {
		return map[string]any{}, nil
	}
	var data any
	if err := util.DecodeJSON(resp.Body, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func registerExpectedStatus(status int, expected ...int) bool {
	for _, item := range expected {
		if status == item {
			return true
		}
	}
	return false
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
