package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/url"
	"strconv"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(in)
	if err != nil {
		return util.CopyMap(in)
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return util.CopyMap(in)
	}
	return out
}

func registerRandomPassword(length int) string {
	if length < 8 {
		length = 8
	}
	upper := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lower := "abcdefghijklmnopqrstuvwxyz"
	digits := "0123456789"
	special := "!@#$%"
	all := upper + lower + digits + special
	value := []byte{
		upper[mathrand.Intn(len(upper))],
		lower[mathrand.Intn(len(lower))],
		digits[mathrand.Intn(len(digits))],
		special[mathrand.Intn(len(special))],
	}
	for len(value) < length {
		value = append(value, all[mathrand.Intn(len(all))])
	}
	mathrand.Shuffle(len(value), func(i, j int) {
		value[i], value[j] = value[j], value[i]
	})
	return string(value)
}

func registerRandomName() (string, string) {
	return registerFirstNames[mathrand.Intn(len(registerFirstNames))], registerLastNames[mathrand.Intn(len(registerLastNames))]
}

func registerRandomBirthdate() string {
	year := 1996 + mathrand.Intn(11)
	month := 1 + mathrand.Intn(12)
	day := 1 + mathrand.Intn(28)
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func registerRandomToken() string {
	return util.RandomTokenURL(24)
}

func registerPKCEChallenge() string {
	_, challenge := generateRegisterPKCE()
	return challenge
}

func generateRegisterPKCE() (string, string) {
	buf := make([]byte, 64)
	_, _ = rand.Read(buf)
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func registerAuthorizeParams(email, deviceID, state, nonce, codeChallenge string) url.Values {
	values := url.Values{}
	values.Set("issuer", registerAuthBase)
	values.Set("client_id", registerPlatformOAuthClientID)
	values.Set("audience", registerPlatformOAuthAudience)
	values.Set("redirect_uri", registerPlatformOAuthRedirectURI)
	values.Set("device_id", deviceID)
	values.Set("screen_hint", "login_or_signup")
	values.Set("max_age", "0")
	values.Set("login_hint", email)
	values.Set("scope", "openid profile email offline_access")
	values.Set("response_type", "code")
	values.Set("response_mode", "query")
	values.Set("state", state)
	values.Set("nonce", nonce)
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	values.Set("auth0Client", registerPlatformAuth0Client)
	return values
}

func registerOAuthCode(target string) string {
	if strings.TrimSpace(target) == "" {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("code"))
}

func resolveRegisterLocation(baseURL, location string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	next, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(next).String(), nil
}

func newRegisterSentinelTokenGenerator(deviceID, userAgent string) *registerSentinelTokenGenerator {
	return &registerSentinelTokenGenerator{
		deviceID:  deviceID,
		userAgent: userAgent,
		sid:       util.NewUUID(),
	}
}

func (g *registerSentinelTokenGenerator) config() []any {
	perfNow := 1000 + mathrand.Float64()*49000
	return []any{
		"1920x1080",
		time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"),
		int64(4294705152),
		mathrand.Float64(),
		g.userAgent,
		registerSentinelSDK,
		nil,
		nil,
		"en-US",
		mathrand.Float64(),
		registerRandomChoice([]string{"vendorSub-undefined", "plugins-undefined", "mimeTypes-undefined", "hardwareConcurrency-undefined"}),
		registerRandomChoice([]string{"location", "implementation", "URL", "documentURI", "compatMode"}),
		registerRandomChoice([]string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}),
		perfNow,
		g.sid,
		"",
		registerRandomChoiceInt([]int{4, 8, 12, 16}),
		float64(time.Now().UnixMilli()) - perfNow,
	}
}

func (g *registerSentinelTokenGenerator) generateRequirementsToken() string {
	data := g.config()
	data[3] = 1
	data[9] = math.Round(5 + mathrand.Float64()*45)
	return "gAAAAAC" + registerBase64JSON(data)
}

func (g *registerSentinelTokenGenerator) generateToken(seed, difficulty string) string {
	start := time.Now()
	data := g.config()
	if difficulty == "" {
		difficulty = "0"
	}
	for i := 0; i < registerSentinelMaxAttempts; i++ {
		data[3] = i
		data[9] = math.Round(float64(time.Since(start).Milliseconds()))
		payload := registerBase64JSON(data)
		hash := registerFNV1A32(seed + payload)
		prefixLen := minInt(len(difficulty), len(hash))
		if hash[:prefixLen] <= difficulty[:prefixLen] {
			return "gAAAAAB" + payload + "~S"
		}
	}
	return "gAAAAAB" + registerSentinelErrorPrefix + registerBase64JSON("None")
}

func registerBase64JSON(value any) string {
	data, err := registerCompactJSONBytes(value)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

func registerCompactJSONBytes(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func registerFNV1A32(text string) string {
	hash := uint32(2166136261)
	for _, ch := range text {
		hash ^= uint32(ch)
		hash *= 16777619
	}
	hash ^= hash >> 16
	hash *= 2246822507
	hash ^= hash >> 13
	hash *= 3266489909
	hash ^= hash >> 16
	return fmt.Sprintf("%08x", hash)
}

func registerSentinelHeaders() map[string]string {
	return map[string]string{
		"Content-Type":       "text/plain;charset=UTF-8",
		"Referer":            registerSentinelBase + "/backend-api/sentinel/frame.html",
		"Origin":             registerSentinelBase,
		"User-Agent":         registerUserAgent,
		"sec-ch-ua":          registerSecCHUA,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
	}
}

func registerTraceHeaders() map[string]string {
	traceID := util.NewHex(32)
	parentID := registerRandomUint64()
	parentHex := fmt.Sprintf("%016x", parentID)
	parentText := strconv.FormatUint(parentID, 10)
	return map[string]string{
		"traceparent":                 "00-" + traceID + "-" + parentHex + "-01",
		"tracestate":                  "dd=s:1;o:rum",
		"x-datadog-origin":            "rum",
		"x-datadog-parent-id":         parentText,
		"x-datadog-sampling-priority": "1",
		"x-datadog-trace-id":          strconv.FormatUint(registerRandomUint64(), 10),
	}
}

func registerRandomUint64() uint64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return uint64(mathrand.Int63())
	}
	var value uint64
	for _, b := range buf {
		value = (value << 8) | uint64(b)
	}
	return value
}

func registerRandomChoice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[mathrand.Intn(len(values))]
}

func registerRandomChoiceInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[mathrand.Intn(len(values))]
}
