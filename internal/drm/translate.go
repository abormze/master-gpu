package drm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gpupanel/internal/models"
)

// Live subtitle translation. Providers: google (free, no key), libre
// (LibreTranslate URL+key), deepl (key) and openai (any OpenAI-compatible
// chat endpoint: URL, key, model).

type translator struct {
	mu    sync.Mutex
	cache map[string]string
	hc    *http.Client
	pause time.Time
}

var tr = &translator{cache: map[string]string{}, hc: &http.Client{Timeout: 8 * time.Second,
	// Google's free endpoint rate-limits Go's HTTP/2 client; HTTP/1.1 works.
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}}}

func (t *translator) translate(ctx context.Context, st models.Settings, text, to string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || to == "" {
		return text, nil
	}
	p := strings.ToLower(st.TranslateProvider)
	if p == "" {
		p = "google"
	}
	k := p + "|" + to + "|" + text
	t.mu.Lock()
	if v, ok := t.cache[k]; ok {
		t.mu.Unlock()
		return v, nil
	}
	if time.Now().Before(t.pause) {
		t.mu.Unlock()
		return "", fmt.Errorf("translate rate limited")
	}
	t.mu.Unlock()
	var out string
	var err error
	switch p {
	case "libre":
		out, err = t.libre(ctx, st, text, to)
	case "deepl":
		out, err = t.deepl(ctx, st, text, to)
	case "openai":
		out, err = t.openai(ctx, st, text, to)
	default:
		out, err = t.google(ctx, text, to)
	}
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	t.mu.Lock()
	if len(t.cache) > 20000 {
		t.cache = map[string]string{}
	}
	t.cache[k] = out
	t.mu.Unlock()
	return out, nil
}

func (t *translator) do(req *http.Request, v any) error {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 429 {
		t.mu.Lock()
		t.pause = time.Now().Add(time.Minute)
		t.mu.Unlock()
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("translate HTTP %d: %.200s", resp.StatusCode, b)
	}
	return json.Unmarshal(b, v)
}

func (t *translator) google(ctx context.Context, text, to string) (string, error) {
	u := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&dt=t&tl=" + url.QueryEscape(to) + "&q=" + url.QueryEscape(text)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	var r []any
	if err := t.do(req, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	if len(r) > 0 {
		parts, _ := r[0].([]any)
		for _, p := range parts {
			if x, ok := p.([]any); ok && len(x) > 0 {
				s, _ := x[0].(string)
				b.WriteString(s)
			}
		}
	}
	return b.String(), nil
}

func jsonReq(ctx context.Context, u string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (t *translator) libre(ctx context.Context, st models.Settings, text, to string) (string, error) {
	base := strings.TrimRight(firstNonEmpty(st.TranslateURL, "http://127.0.0.1:5000"), "/")
	req := jsonReq(ctx, base+"/translate", map[string]string{"q": text, "source": "auto", "target": to, "format": "text", "api_key": st.TranslateKey})
	var r struct {
		TranslatedText string `json:"translatedText"`
	}
	err := t.do(req, &r)
	return r.TranslatedText, err
}

func (t *translator) deepl(ctx context.Context, st models.Settings, text, to string) (string, error) {
	u := "https://api.deepl.com/v2/translate"
	if strings.HasSuffix(st.TranslateKey, ":fx") {
		u = "https://api-free.deepl.com/v2/translate"
	}
	if st.TranslateURL != "" {
		u = st.TranslateURL
	}
	req := jsonReq(ctx, u, map[string]any{"text": []string{text}, "target_lang": strings.ToUpper(to)})
	req.Header.Set("Authorization", "DeepL-Auth-Key "+st.TranslateKey)
	var r struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := t.do(req, &r); err != nil {
		return "", err
	}
	if len(r.Translations) == 0 {
		return "", fmt.Errorf("deepl: empty response")
	}
	return r.Translations[0].Text, nil
}

func (t *translator) openai(ctx context.Context, st models.Settings, text, to string) (string, error) {
	base := strings.TrimRight(firstNonEmpty(st.TranslateURL, "https://api.openai.com/v1"), "/")
	req := jsonReq(ctx, base+"/chat/completions", map[string]any{
		"model":       firstNonEmpty(st.TranslateModel, "gpt-4o-mini"),
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "Translate the TV subtitle the user sends into language code '" + to + "'. Keep line breaks. Reply with the translation only."},
			{"role": "user", "content": text},
		},
	})
	req.Header.Set("Authorization", "Bearer "+st.TranslateKey)
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := t.do(req, &r); err != nil {
		return "", err
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("openai: empty response")
	}
	return r.Choices[0].Message.Content, nil
}
