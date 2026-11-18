package opencodego

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/xiao-qiu-qiu/PlanBridge/internal/vendors"
)

// sessionIDLength is how many hex characters of the digest become the session
// id: 128 bits, well inside a header value and still collision free per key.
const sessionIDLength = 32

// sessionPartLimit caps each prompt excerpt hashed into the session id at 4 KiB.
// The id must be stable per conversation, not a fingerprint of the whole prompt.
const sessionPartLimit = 4 << 10

// requestSession resolves the x-opencode-session value: the client's own session
// header when it sent one (the bridge forwards it as SessionHint), otherwise a
// stable id derived from the credential and the conversation's first system and
// user messages. Multi-turn requests keep the same id because those messages do
// not change; different conversations differ from the first user turn on.
func requestSession(r vendors.Request) string {
	if hint := strings.TrimSpace(r.SessionHint); hint != "" {
		return hint
	}
	system, user := firstMessages(r.Payload)
	if system == "" && user == "" {
		// Nothing usable to identify the conversation. Staying deterministic per
		// credential and model keeps caching stable without merging conversations.
		return hashSession(r.Credential.ID, r.Model)
	}
	return hashSession(r.Credential.ID, system, user)
}

// hashSession joins the parts with a separator that cannot occur in a prompt
// excerpt by accident and truncates the digest to sessionIDLength.
func hashSession(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:sessionIDLength]
}

// firstMessages reads the first system and the first user message of a
// chat-completions payload, "{messages:[{role,content}]}".
func firstMessages(payload []byte) (system, user string) {
	if len(payload) == 0 {
		return "", ""
	}
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", ""
	}
	systemFound, userFound := false, false
	for _, message := range body.Messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "system":
			if !systemFound {
				systemFound, system = true, messageText(message.Content)
			}
		case "user":
			if !userFound {
				userFound, user = true, messageText(message.Content)
			}
		}
	}
	return system, user
}

// messageText reads one message: plain string content, or the text parts of a
// parts array. Image and tool parts contribute nothing to the session id.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return limitBytes(text, sessionPartLimit)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type != "" && part.Type != "text" && part.Type != "input_text" {
			continue
		}
		builder.WriteString(part.Text)
	}
	return limitBytes(builder.String(), sessionPartLimit)
}

// limitBytes truncates s to at most n bytes without splitting a UTF-8 sequence.
func limitBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
