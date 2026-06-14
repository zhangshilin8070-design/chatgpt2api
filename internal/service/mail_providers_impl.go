package service

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"chatgpt2api/internal/util"
)

func (p *registerCloudflareTempMailProvider) CreateMailbox(username string) (map[string]any, error) {
	apiBase := strings.TrimRight(util.Clean(p.entry["api_base"]), "/")
	adminPassword := util.Clean(p.entry["admin_password"])
	domain, err := nextRegisterDomain(util.AsStringSlice(p.entry["domain"]), util.AsStringSlice(p.entry["blocked_domains"])...)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"enablePrefix": true,
		"name":         firstNonEmpty(strings.TrimSpace(username), registerRandomMailboxName()),
		"domain":       domain,
	}
	data, err := registerMailRequestJSON(p.client, http.MethodPost, apiBase+"/admin/new_address", map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   p.conf.UserAgent,
		"x-admin-auth": adminPassword,
	}, nil, payload, http.StatusOK)
	if err != nil {
		return nil, err
	}
	address := util.Clean(data["address"])
	token := util.Clean(data["jwt"])
	if address == "" || token == "" {
		return nil, fmt.Errorf("cloudflare_temp_email response missing address or jwt")
	}
	return map[string]any{"provider": "cloudflare_temp_email", "provider_ref": p.entry["provider_ref"], "address": address, "token": token}, nil
}

func (p *registerCloudflareTempMailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	apiBase := strings.TrimRight(util.Clean(p.entry["api_base"]), "/")
	token := util.Clean(mailbox["token"])
	data, err := registerMailRequestJSON(p.client, http.MethodGet, apiBase+"/api/mails", map[string]string{
		"Authorization": "Bearer " + token,
		"User-Agent":    p.conf.UserAgent,
	}, map[string]string{"limit": "10", "offset": "0"}, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	items := util.AsMapSlice(data["results"])
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if registerMessageMatchesEmail(item, util.Clean(mailbox["address"])) {
			messages = append(messages, item)
		}
	}
	if len(messages) == 0 {
		return nil, nil
	}
	message := latestRegisterMailMessage(messages)
	textContent, htmlContent := extractRegisterMailContent(message)
	sender := firstNonNil(message["from"], message["sender"])
	if senderMap, ok := sender.(map[string]any); ok {
		sender = firstNonNil(senderMap["address"], senderMap["email"], senderMap["name"])
	}
	return map[string]any{
		"provider":     "cloudflare_temp_email",
		"mailbox":      util.Clean(mailbox["address"]),
		"message_id":   firstNonEmpty(util.Clean(message["id"]), util.Clean(message["_id"])),
		"subject":      util.Clean(message["subject"]),
		"sender":       util.Clean(sender),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(message["createdAt"], message["created_at"], message["receivedAt"], message["date"], message["timestamp"]),
		"raw":          message,
	}, nil
}

func (p *registerTempMailLOLProvider) CreateMailbox(username string) (map[string]any, error) {
	payload := map[string]any{}
	domains := util.AsStringSlice(p.entry["domain"])
	if len(domains) > 0 {
		domain := domains[rand.Intn(len(domains))]
		if strings.HasPrefix(domain, "*.") && len(domain) > 2 {
			payload["domain"] = registerRandomSubdomainLabel() + "." + strings.TrimPrefix(domain, "*.")
			payload["prefix"] = registerRandomMailboxName()
		} else if strings.TrimSpace(domain) != "" {
			payload["domain"] = strings.TrimSpace(domain)
		}
	}
	if username = strings.TrimSpace(username); username != "" && payload["prefix"] == nil {
		payload["prefix"] = username
	}
	data, err := registerMailRequestJSON(p.client, http.MethodPost, "https://api.tempmail.lol/v2/inbox/create", map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
		"Authorization": func() string {
			if key := util.Clean(p.entry["api_key"]); key != "" {
				return "Bearer " + key
			}
			return ""
		}(),
	}, nil, payload, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	address := util.Clean(data["address"])
	token := util.Clean(data["token"])
	if address == "" || token == "" {
		return nil, fmt.Errorf("tempmail_lol response missing address or token")
	}
	return map[string]any{"provider": "tempmail_lol", "provider_ref": p.entry["provider_ref"], "address": address, "token": token}, nil
}

func (p *registerTempMailLOLProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	data, err := registerMailRequestJSON(p.client, http.MethodGet, "https://api.tempmail.lol/v2/inbox", map[string]string{
		"User-Agent": p.conf.UserAgent,
		"Accept":     "application/json",
	}, map[string]string{"token": util.Clean(mailbox["token"])}, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	items := util.AsMapSlice(firstNonNil(data["emails"], data["messages"]))
	if len(items) == 0 {
		return nil, nil
	}
	latest := latestRegisterMailMessage(items)
	textContent, htmlContent := extractRegisterMailContent(latest)
	return map[string]any{
		"provider":     "tempmail_lol",
		"mailbox":      util.Clean(mailbox["address"]),
		"message_id":   firstNonEmpty(util.Clean(latest["id"]), util.Clean(latest["token"])),
		"subject":      util.Clean(latest["subject"]),
		"sender":       firstNonEmpty(util.Clean(latest["from"]), util.Clean(latest["from_address"])),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(latest["created_at"], latest["createdAt"], latest["date"], latest["received_at"], latest["timestamp"]),
		"raw":          latest,
	}, nil
}

func (p *registerDuckMailProvider) CreateMailbox(username string) (map[string]any, error) {
	apiKey := util.Clean(p.entry["api_key"])
	domain := util.Clean(p.entry["default_domain"])
	if domain == "" {
		domain = "duckmail.sbs"
	}
	password := randomAlphaNum(12)
	address := firstNonEmpty(strings.TrimSpace(username), registerRandomMailboxName()) + "@" + domain
	payload := map[string]any{"address": address, "password": password}
	account, err := registerMailRequestJSON(p.client, http.MethodPost, "https://api.duckmail.sbs/accounts", map[string]string{
		"Authorization": "Bearer " + apiKey,
		"User-Agent":    p.conf.UserAgent,
		"Accept":        "application/json",
		"Content-Type":  "application/json",
	}, nil, payload, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	tokenData, err := registerMailRequestJSON(p.client, http.MethodPost, "https://api.duckmail.sbs/token", map[string]string{
		"Authorization": "Bearer " + apiKey,
		"User-Agent":    p.conf.UserAgent,
		"Accept":        "application/json",
		"Content-Type":  "application/json",
	}, nil, payload, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider":     "duckmail",
		"provider_ref": p.entry["provider_ref"],
		"address":      address,
		"token":        util.Clean(tokenData["token"]),
		"password":     password,
		"account_id":   util.Clean(account["id"]),
	}, nil
}

func (p *registerDuckMailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	token := util.Clean(mailbox["token"])
	data, err := registerMailRequestAny(p.client, http.MethodGet, "https://api.duckmail.sbs/messages", map[string]string{
		"Authorization": "Bearer " + token,
		"User-Agent":    p.conf.UserAgent,
		"Accept":        "application/json",
	}, map[string]string{"page": "1"}, nil, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	items := duckMailItems(data)
	if len(items) == 0 {
		return nil, nil
	}
	messageID := strings.TrimPrefix(util.Clean(firstNonNil(items[0]["id"], items[0]["@id"])), "/messages/")
	if messageID == "" {
		return nil, nil
	}
	message, err := registerMailRequestJSON(p.client, http.MethodGet, "https://api.duckmail.sbs/messages/"+messageID, map[string]string{
		"Authorization": "Bearer " + token,
		"User-Agent":    p.conf.UserAgent,
		"Accept":        "application/json",
	}, nil, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	textContent, htmlContent := extractRegisterMailContent(message)
	sender := message["from"]
	if senderMap, ok := sender.(map[string]any); ok {
		sender = firstNonNil(senderMap["address"], senderMap["name"])
	}
	return map[string]any{
		"provider":     "duckmail",
		"mailbox":      util.Clean(mailbox["address"]),
		"message_id":   messageID,
		"subject":      util.Clean(message["subject"]),
		"sender":       util.Clean(sender),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(message["createdAt"], message["created_at"], message["receivedAt"], message["date"]),
		"raw":          message,
	}, nil
}

func (p *registerGPTMailProvider) CreateMailbox(username string) (map[string]any, error) {
	payload := map[string]any{}
	if username = strings.TrimSpace(username); username != "" {
		payload["prefix"] = username
	}
	if domain := util.Clean(p.entry["default_domain"]); domain != "" {
		payload["domain"] = domain
	}
	method := http.MethodGet
	var requestBody any
	if len(payload) > 0 {
		method = http.MethodPost
		requestBody = payload
	}
	data, err := registerMailRequestAny(p.client, method, "https://mail.chatgpt.org.uk/api/generate-email", map[string]string{
		"X-API-Key":    util.Clean(p.entry["api_key"]),
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}, nil, requestBody, http.StatusOK)
	if err != nil {
		return nil, err
	}
	typed := util.StringMap(data)
	payloadMap := util.StringMap(firstNonNil(typed["data"], data))
	address := util.Clean(payloadMap["email"])
	if address == "" {
		return nil, fmt.Errorf("gptmail response missing email")
	}
	return map[string]any{"provider": "gptmail", "provider_ref": p.entry["provider_ref"], "address": address}, nil
}

func (p *registerGPTMailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	data, err := registerMailRequestAny(p.client, http.MethodGet, "https://mail.chatgpt.org.uk/api/emails", map[string]string{
		"X-API-Key":  util.Clean(p.entry["api_key"]),
		"User-Agent": p.conf.UserAgent,
		"Accept":     "application/json",
	}, map[string]string{"email": util.Clean(mailbox["address"])}, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	body := util.StringMap(data)
	if nested := util.StringMap(body["data"]); len(nested) > 0 {
		body = nested
	}
	items := util.AsMapSlice(firstNonNil(body["emails"], body))
	if len(items) == 0 {
		return nil, nil
	}
	latest := latestRegisterMailMessage(items)
	if id := util.Clean(latest["id"]); id != "" {
		detail, detailErr := registerMailRequestAny(p.client, http.MethodGet, "https://mail.chatgpt.org.uk/api/email/"+id, map[string]string{
			"X-API-Key":  util.Clean(p.entry["api_key"]),
			"User-Agent": p.conf.UserAgent,
			"Accept":     "application/json",
		}, nil, nil, http.StatusOK)
		if detailErr == nil {
			if typed, ok := detail.(map[string]any); ok && typed["data"] != nil {
				latest = util.StringMap(typed["data"])
			} else if typed, ok := detail.(map[string]any); ok {
				latest = typed
			}
		}
	}
	textContent, htmlContent := extractRegisterMailContent(latest)
	return map[string]any{
		"provider":     "gptmail",
		"mailbox":      util.Clean(mailbox["address"]),
		"message_id":   util.Clean(latest["id"]),
		"subject":      util.Clean(latest["subject"]),
		"sender":       util.Clean(latest["from_address"]),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(latest["timestamp"], latest["created_at"]),
		"raw":          latest,
	}, nil
}

func (p *registerMoEmailProvider) CreateMailbox(username string) (map[string]any, error) {
	apiBase := strings.TrimRight(util.Clean(p.entry["api_base"]), "/")
	if apiBase == "" {
		return nil, fmt.Errorf("moemail api_base is required")
	}
	domain, err := nextRegisterDomain(util.AsStringSlice(p.entry["domain"]), util.AsStringSlice(p.entry["blocked_domains"])...)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"name":       firstNonEmpty(strings.TrimSpace(username), registerRandomMailboxName()),
		"expiryTime": util.ToInt(p.entry["expiry_time"], 0),
		"domain":     domain,
	}
	data, err := registerMailRequestJSON(p.client, http.MethodPost, apiBase+"/api/emails/generate", map[string]string{
		"X-API-Key":    util.Clean(p.entry["api_key"]),
		"Content-Type": "application/json",
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
	}, nil, payload, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	address := util.Clean(data["email"])
	emailID := firstNonEmpty(util.Clean(data["id"]), util.Clean(data["email_id"]))
	if address == "" || emailID == "" {
		return nil, fmt.Errorf("MoEmail missing email or id")
	}
	return map[string]any{"provider": "moemail", "provider_ref": p.entry["provider_ref"], "address": address, "email_id": emailID}, nil
}

func (p *registerMoEmailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	apiBase := strings.TrimRight(util.Clean(p.entry["api_base"]), "/")
	emailID := util.Clean(mailbox["email_id"])
	if apiBase == "" {
		return nil, fmt.Errorf("moemail api_base is required")
	}
	if emailID == "" {
		return nil, fmt.Errorf("MoEmail missing email_id")
	}
	data, err := registerMailRequestJSON(p.client, http.MethodGet, apiBase+"/api/emails/"+emailID, map[string]string{
		"X-API-Key":    util.Clean(p.entry["api_key"]),
		"Content-Type": "application/json",
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
	}, nil, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	items := util.AsMapSlice(data["messages"])
	if len(items) == 0 {
		return nil, nil
	}
	latest := latestRegisterMailMessage(items)
	messageID := firstNonEmpty(util.Clean(latest["id"]), util.Clean(latest["message_id"]), util.Clean(latest["_id"]))
	message := latest
	raw := any(data)
	if messageID != "" {
		detail, detailErr := registerMailRequestJSON(p.client, http.MethodGet, apiBase+"/api/emails/"+emailID+"/"+messageID, map[string]string{
			"X-API-Key":    util.Clean(p.entry["api_key"]),
			"Content-Type": "application/json",
			"User-Agent":   p.conf.UserAgent,
			"Accept":       "application/json",
		}, nil, nil, http.StatusOK)
		if detailErr != nil {
			return nil, detailErr
		}
		raw = detail
		if nested := util.StringMap(detail["message"]); len(nested) > 0 {
			message = nested
		} else {
			message = detail
		}
	}
	textContent, htmlContent := extractRegisterMailContent(message)
	sender := firstNonNil(message["from"], message["sender"])
	if senderMap, ok := sender.(map[string]any); ok {
		sender = firstNonNil(senderMap["address"], senderMap["email"], senderMap["name"])
	}
	return map[string]any{
		"provider":     "moemail",
		"mailbox":      util.Clean(mailbox["address"]),
		"message_id":   messageID,
		"subject":      firstNonEmpty(util.Clean(message["subject"]), util.Clean(latest["subject"])),
		"sender":       util.Clean(sender),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(message["createdAt"], message["created_at"], message["receivedAt"], message["date"], message["timestamp"], latest["createdAt"], latest["created_at"], latest["receivedAt"], latest["date"], latest["timestamp"]),
		"raw":          raw,
	}, nil
}

func (p *registerInbucketMailProvider) CreateMailbox(username string) (map[string]any, error) {
	apiBase := strings.TrimRight(util.Clean(p.entry["api_base"]), "/")
	if apiBase == "" {
		return nil, fmt.Errorf("inbucket api_base is required")
	}
	baseDomain, err := nextRegisterDomain(util.AsStringSlice(p.entry["domain"]), util.AsStringSlice(p.entry["blocked_domains"])...)
	if err != nil {
		return nil, err
	}
	localPart := firstNonEmpty(strings.TrimSpace(username), registerRandomMailboxName())
	domain := baseDomain
	randomSubdomain := true
	if _, ok := p.entry["random_subdomain"]; ok {
		randomSubdomain = util.ToBool(p.entry["random_subdomain"])
	}
	if randomSubdomain {
		domain = registerRandomSubdomainLabel() + "." + baseDomain
	}
	address := localPart + "@" + domain
	return map[string]any{
		"provider":     "inbucket",
		"provider_ref": p.entry["provider_ref"],
		"address":      address,
		"base_domain":  baseDomain,
		"mailbox_name": localPart,
	}, nil
}

func (p *registerInbucketMailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	apiBase := strings.TrimRight(util.Clean(p.entry["api_base"]), "/")
	if apiBase == "" {
		return nil, fmt.Errorf("inbucket api_base is required")
	}
	mailboxName := util.Clean(mailbox["mailbox_name"])
	if mailboxName == "" {
		mailboxName = registerInbucketMailboxName(util.Clean(mailbox["address"]))
	}
	if mailboxName == "" {
		return nil, fmt.Errorf("inbucket missing mailbox_name")
	}
	data, err := registerMailRequestAny(p.client, http.MethodGet, apiBase+"/api/v1/mailbox/"+url.PathEscape(mailboxName), map[string]string{
		"User-Agent": p.conf.UserAgent,
		"Accept":     "application/json",
	}, nil, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	items := util.AsMapSlice(data)
	if len(items) == 0 {
		return nil, nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := registerMessageReceivedAt(items[i])
		right := registerMessageReceivedAt(items[j])
		if !left.IsZero() || !right.IsZero() {
			if !left.Equal(right) {
				return left.After(right)
			}
			return registerMessageID(items[i]) > registerMessageID(items[j])
		}
		return false
	})
	address := util.Clean(mailbox["address"])
	for _, item := range items {
		messageID := util.Clean(item["id"])
		if messageID == "" {
			continue
		}
		detail, detailErr := registerMailRequestJSON(p.client, http.MethodGet, apiBase+"/api/v1/mailbox/"+url.PathEscape(mailboxName)+"/"+url.PathEscape(messageID), map[string]string{
			"User-Agent": p.conf.UserAgent,
			"Accept":     "application/json",
		}, nil, nil, http.StatusOK)
		if detailErr != nil {
			return nil, detailErr
		}
		header := util.StringMap(detail["header"])
		body := util.StringMap(detail["body"])
		normalized := map[string]any{
			"provider":     "inbucket",
			"mailbox":      mailboxName,
			"message_id":   messageID,
			"subject":      firstNonEmpty(util.Clean(detail["subject"]), util.Clean(item["subject"])),
			"sender":       firstNonEmpty(util.Clean(detail["from"]), util.Clean(item["from"])),
			"text_content": util.Clean(body["text"]),
			"html_content": util.Clean(body["html"]),
			"received_at":  firstNonNil(detail["date"], item["date"]),
			"to":           firstNonNil(header["To"], header["to"]),
			"raw":          detail,
		}
		if registerMessageMatchesEmail(normalized, address) {
			return normalized, nil
		}
	}
	return nil, nil
}

func registerInbucketMailboxName(address string) string {
	localPart, _, _ := strings.Cut(strings.TrimSpace(address), "@")
	return strings.TrimSpace(localPart)
}

func (p *registerYYDSMailProvider) CreateMailbox(username string) (map[string]any, error) {
	payload := map[string]any{"localPart": firstNonEmpty(strings.TrimSpace(username), registerRandomMailboxName())}
	if domains := util.AsStringSlice(p.entry["domain"]); len(domains) > 0 {
		blocked := util.AsStringSlice(p.entry["blocked_domains"])
		domain, err := nextRegisterDomain(domains, blocked...)
		if err != nil {
			return nil, err
		}
		payload["domain"] = domain
	}
	if subdomain := util.Clean(p.entry["subdomain"]); subdomain != "" {
		payload["subdomain"] = subdomain
	}
	path := "/accounts"
	if util.ToBool(p.entry["wildcard"]) {
		path = "/accounts/wildcard"
	}
	data, err := p.request(http.MethodPost, path, "", nil, payload, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return nil, err
	}
	body := util.StringMap(data)
	address := firstNonEmpty(util.Clean(body["address"]), util.Clean(body["email"]))
	token := firstNonEmpty(util.Clean(body["token"]), util.Clean(body["temp_token"]), util.Clean(body["tempToken"]), util.Clean(body["access_token"]))
	if address == "" || token == "" {
		return nil, fmt.Errorf("YYDSMail missing address or token")
	}
	return map[string]any{
		"provider":     "yyds_mail",
		"provider_ref": p.entry["provider_ref"],
		"address":      address,
		"token":        token,
		"account_id":   util.Clean(body["id"]),
	}, nil
}

func (p *registerYYDSMailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	token := util.Clean(mailbox["token"])
	if token == "" {
		return nil, fmt.Errorf("YYDSMail missing token")
	}
	data, err := p.request(http.MethodGet, "/messages", token, map[string]string{"address": util.Clean(mailbox["address"])}, nil, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return nil, err
	}
	items := yydsMailItems(data)
	if len(items) == 0 {
		return nil, nil
	}
	latest := latestRegisterMailMessage(items)
	messageID := firstNonEmpty(util.Clean(latest["id"]), util.Clean(latest["message_id"]))
	message := latest
	raw := any(latest)
	if messageID != "" {
		detail, detailErr := p.request(http.MethodGet, "/messages/"+url.PathEscape(messageID), token, map[string]string{"address": util.Clean(mailbox["address"])}, nil, http.StatusOK, http.StatusCreated, http.StatusNoContent)
		if detailErr != nil {
			return nil, detailErr
		}
		raw = detail
		if detailMap := util.StringMap(detail); len(detailMap) > 0 {
			message = detailMap
		}
	}
	textContent, htmlContent := extractRegisterMailContent(message)
	sender := firstNonNil(message["from"], message["sender"])
	if senderMap, ok := sender.(map[string]any); ok {
		sender = firstNonNil(senderMap["address"], senderMap["email"], senderMap["name"])
	}
	return map[string]any{
		"provider":     "yyds_mail",
		"mailbox":      util.Clean(mailbox["address"]),
		"message_id":   messageID,
		"subject":      util.Clean(message["subject"]),
		"sender":       util.Clean(sender),
		"text_content": textContent,
		"html_content": htmlContent,
		"received_at":  firstNonNil(message["createdAt"], message["created_at"], message["receivedAt"], message["date"], message["timestamp"]),
		"raw":          raw,
	}, nil
}

func (p *registerYYDSMailProvider) request(method, path, token string, query map[string]string, payload any, expected ...int) (any, error) {
	apiBase := strings.TrimRight(firstNonEmpty(util.Clean(p.entry["api_base"]), "https://maliapi.215.im/v1"), "/")
	headers := map[string]string{
		"User-Agent":   p.conf.UserAgent,
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	} else {
		headers["X-API-Key"] = util.Clean(p.entry["api_key"])
	}
	data, err := registerMailRequestAny(p.client, method, apiBase+path, headers, query, payload, expected...)
	if err != nil {
		return nil, err
	}
	body, ok := data.(map[string]any)
	if !ok {
		return data, nil
	}
	if success, exists := body["success"]; exists && !util.ToBool(success) {
		return nil, fmt.Errorf("YYDSMail request failed: %s", firstNonEmpty(util.Clean(body["errorCode"]), util.Clean(body["error"]), util.Clean(body["message"]), "unknown error"))
	}
	if nested, exists := body["data"]; exists {
		switch nested.(type) {
		case map[string]any, []any:
			return nested, nil
		}
	}
	return data, nil
}

func (p *registerHLOOLMailProvider) CreateMailbox(username string) (map[string]any, error) {
	apiBase := strings.TrimRight(firstNonEmpty(util.Clean(p.entry["api_base"]), "https://email.hlool.cc"), "/")
	payload := map[string]any{}
	if username = strings.TrimSpace(username); username != "" {
		payload["prefix"] = username
	}
	if domains := util.AsStringSlice(p.entry["domain"]); len(domains) > 0 {
		blocked := util.AsStringSlice(p.entry["blocked_domains"])
		domain, err := nextRegisterDomain(domains, blocked...)
		if err != nil {
			return nil, err
		}
		payload["domain"] = domain
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second) // 1s, 2s
		}
		data, err := registerMailRequestAny(p.client, http.MethodPost, apiBase+"/api/generate-email", map[string]string{
			"X-API-Key":    util.Clean(p.entry["api_key"]),
			"User-Agent":   p.conf.UserAgent,
			"Accept":       "application/json",
			"Content-Type": "application/json",
		}, nil, payload, http.StatusOK, http.StatusCreated)
		if err != nil {
			lastErr = err
			if !isHloolRetryable(err) {
				break
			}
			continue
		}
		body, ok := data.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hlool_mail unexpected response type")
		}
		if success, exists := body["success"]; exists && !util.ToBool(success) {
			return nil, fmt.Errorf("hlool_mail: %s", firstNonEmpty(util.Clean(body["error"]), "unknown error"))
		}
		payloadMap := util.StringMap(firstNonNil(body["data"], body))
		address := util.Clean(payloadMap["email"])
		if address == "" {
			return nil, fmt.Errorf("hlool_mail response missing email")
		}
		return map[string]any{"provider": "hlool_mail", "provider_ref": p.entry["provider_ref"], "address": address}, nil
	}
	return nil, lastErr
}

func isHloolRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "forcibly closed") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "HTTP 429") ||
		strings.Contains(s, "HTTP 502") ||
		strings.Contains(s, "HTTP 503") ||
		strings.Contains(s, "HTTP 504") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "EOF")
}

func (p *registerHLOOLMailProvider) FetchLatestMessage(mailbox map[string]any) (map[string]any, error) {
	apiBase := strings.TrimRight(firstNonEmpty(util.Clean(p.entry["api_base"]), "https://email.hlool.cc"), "/")

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second) // 1s, 2s
		}
		data, err := registerMailRequestAny(p.client, http.MethodGet, apiBase+"/api/emails/next", map[string]string{
			"X-API-Key":  util.Clean(p.entry["api_key"]),
			"User-Agent": p.conf.UserAgent,
			"Accept":     "application/json",
		}, map[string]string{"email": util.Clean(mailbox["address"])}, nil, http.StatusOK)
		if err != nil {
			lastErr = err
			if !isHloolRetryable(err) {
				break
			}
			continue
		}
		body, ok := data.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hlool_mail unexpected response type")
		}
		if success, exists := body["success"]; exists && !util.ToBool(success) {
			return nil, fmt.Errorf("hlool_mail: %s", firstNonEmpty(util.Clean(body["error"]), "unknown error"))
		}
		payloadMap := util.StringMap(firstNonNil(body["data"], body))
		if !util.ToBool(payloadMap["has_email"]) {
			return nil, nil
		}
		message := util.StringMap(payloadMap["message"])
		if len(message) == 0 {
			return nil, nil
		}
		textContent, htmlContent := extractRegisterMailContent(message)
		return map[string]any{
			"provider":     "hlool_mail",
			"mailbox":      util.Clean(mailbox["address"]),
			"message_id":   util.Clean(message["id"]),
			"subject":      util.Clean(message["subject"]),
			"sender":       util.Clean(message["from_address"]),
			"text_content": textContent,
			"html_content": htmlContent,
			"received_at":  firstNonNil(message["created_at"], message["createdAt"], message["timestamp"]),
			"raw":          message,
		}, nil
	}
	return nil, lastErr
}
