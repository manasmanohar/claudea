package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	liveSvc  = "Claude Code-credentials"
	slotSvc  = "claude-acct"
	tokenURL = "https://platform.claude.com/v1/oauth/token"
	clientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	usageURL = "https://api.anthropic.com/api/oauth/usage"
)

var (
	home, _    = os.UserHomeDir()
	claudeJSON = filepath.Join(home, ".claude.json")
	store      = filepath.Join(home, ".claude-accounts")
	osUser     = func() string { u, _ := user.Current(); return u.Username }()
)

type profile map[string]any

func (p profile) str(k string) string { s, _ := p[k].(string); return s }

type window struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type usage struct {
	FiveHour *window `json:"five_hour"`
	SevenDay *window `json:"seven_day"`
}

var (
	errRateLimited = errors.New("rate limited")
	errOffline     = errors.New("can't reach Anthropic — check your connection")
	errUnsaved     = errors.New("the signed-in account isn't saved yet")
)

type cachedUsage struct {
	Usage   *usage    `json:"usage"`
	At      time.Time `json:"at"`
	Tried   time.Time `json:"tried"`
	Limited bool      `json:"limited"`
}

func loadCache() map[string]cachedUsage {
	c := map[string]cachedUsage{}
	if b, err := os.ReadFile(filepath.Join(store, "usage.cache")); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func saveCache(c map[string]cachedUsage) {
	if b, err := json.Marshal(c); err == nil {
		os.WriteFile(filepath.Join(store, "usage.cache"), b, 0o600)
	}
}

func kcRead(svc, acct string) (string, bool) {
	out, err := exec.Command("security", "find-generic-password", "-s", svc, "-a", acct, "-w").Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func kcWrite(svc, acct, secret string) error {
	out, err := exec.Command("security", "add-generic-password", "-U", "-s", svc, "-a", acct, "-w", secret).CombinedOutput()
	if err != nil {
		return fmt.Errorf("keychain write %s/%s: %s", svc, acct, strings.TrimSpace(string(out)))
	}
	return nil
}

func loadClaudeJSON() (map[string]any, error) {
	b, err := os.ReadFile(claudeJSON)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	return m, d.Decode(&m)
}

func saveClaudeJSON(m map[string]any) error {
	old, err := os.ReadFile(claudeJSON)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(store, "claude.json.bak"), old, 0o600); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := claudeJSON + ".claudea.tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, claudeJSON)
}

func liveProfile() profile {
	m, err := loadClaudeJSON()
	if err != nil {
		return nil
	}
	p, _ := m["oauthAccount"].(map[string]any)
	return p
}

// names lists saved accounts in the order they were added (profile file creation time), so their numbers stay put.
func names() []string {
	ents, _ := os.ReadDir(store)
	var out []string
	born := map[string]int64{}
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, "claude.json") {
			name := strings.TrimSuffix(n, ".json")
			out = append(out, name)
			if info, err := e.Info(); err == nil {
				if st, ok := info.Sys().(*syscall.Stat_t); ok {
					born[name] = st.Birthtimespec.Nano()
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if born[out[i]] != born[out[j]] {
			return born[out[i]] < born[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

func validName(name string) error {
	low := strings.ToLower(name)
	switch {
	case name == "":
		return errors.New("type a name first")
	case strings.ContainsAny(name, "/ \\") || strings.HasPrefix(name, "."):
		return errors.New("use letters, numbers, - or _ (no spaces or slashes)")
	case low == "claude" || strings.HasPrefix(low, "claude.json"):
		return fmt.Errorf("'%s' is reserved — pick another name", name)
	}
	for _, n := range names() {
		if strings.EqualFold(n, name) {
			return fmt.Errorf("'%s' is already used — pick another name", n)
		}
	}
	return nil
}

func loadProfile(name string) profile {
	b, err := os.ReadFile(filepath.Join(store, name+".json"))
	if err != nil {
		return nil
	}
	var p profile
	json.Unmarshal(b, &p)
	return p
}

func activeName() string {
	live := liveProfile()
	if live == nil {
		return ""
	}
	for _, n := range names() {
		if loadProfile(n).str("accountUuid") == live.str("accountUuid") {
			return n
		}
	}
	return ""
}

func saveLiveAs(name string) error {
	creds, ok := kcRead(liveSvc, osUser)
	live := liveProfile()
	if !ok || live == nil {
		return errors.New("not signed in to Claude — run claude, then type /login")
	}
	if saved := loadProfile(name); saved != nil && saved.str("accountUuid") != live.str("accountUuid") {
		return fmt.Errorf("you're now signed in as %s, not '%s' — nothing was changed", live.str("emailAddress"), name)
	}
	if err := os.MkdirAll(store, 0o700); err != nil {
		return err
	}
	if err := kcWrite(slotSvc, name, creds); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(live, "", "  ")
	return os.WriteFile(filepath.Join(store, name+".json"), b, 0o600)
}

func addCurrent(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if cur := activeName(); cur != "" {
		return fmt.Errorf("this account is already saved as '%s'", cur)
	}
	return saveLiveAs(name)
}

func use(name string) error {
	all := names()
	if !slices.Contains(all, name) {
		for _, n := range all {
			if strings.EqualFold(n, name) {
				return fmt.Errorf("no account '%s' — did you mean '%s'?", name, n)
			}
		}
		if len(all) == 0 {
			return errors.New("no saved accounts yet — run claudea to add one")
		}
		return fmt.Errorf("no account '%s' — saved: %s", name, strings.Join(all, ", "))
	}
	p := loadProfile(name)
	m, err := loadClaudeJSON()
	if err != nil {
		return fmt.Errorf("can't read ~/.claude.json (%v) — nothing was changed", err)
	}
	cur := activeName()
	if cur == name {
		return nil
	}
	if cur != "" {
		if err := saveLiveAs(cur); err != nil {
			return err
		}
	} else if liveProfile() != nil {
		return errUnsaved
	}
	creds, ok := kcRead(slotSvc, name)
	if !ok {
		return fmt.Errorf("'%s' has no saved sign-in — press a and sign in to it again", name)
	}
	if err := kcWrite(liveSvc, osUser, creds); err != nil {
		return err
	}
	m["oauthAccount"] = map[string]any(p)
	return saveClaudeJSON(m)
}

func remove(name string) {
	exec.Command("security", "delete-generic-password", "-s", slotSvc, "-a", name).Run()
	os.Remove(filepath.Join(store, name+".json"))
}

func refresh(oauth map[string]any) error {
	body, _ := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "refresh_token": oauth["refreshToken"].(string), "client_id": clientID,
	})
	resp, err := http.Post(tokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return errOffline
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("sign-in expired — press a and sign in to this account again")
	}
	var t struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return err
	}
	oauth["accessToken"] = t.AccessToken
	if t.RefreshToken != "" {
		oauth["refreshToken"] = t.RefreshToken
	}
	oauth["expiresAt"] = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).UnixMilli()
	return nil
}

func getUsage(name string, live bool) (*usage, error) {
	raw, ok := kcRead(slotSvc, name)
	if live {
		raw, ok = kcRead(liveSvc, osUser)
	}
	if !ok {
		return nil, errors.New("no saved credentials")
	}
	var blob map[string]any
	if err := json.Unmarshal([]byte(raw), &blob); err != nil {
		return nil, err
	}
	oauth, _ := blob["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		return nil, errors.New("unexpected credential format")
	}
	exp, _ := oauth["expiresAt"].(float64)
	if int64(exp) < time.Now().Add(time.Minute).UnixMilli() {
		if live {
			return nil, errors.New("sign-in expired — open claude once to renew it")
		}
		if err := refresh(oauth); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(blob)
		if err := kcWrite(slotSvc, name, string(b)); err != nil {
			return nil, err
		}
	}
	req, _ := http.NewRequest("GET", usageURL, nil)
	req.Header.Set("Authorization", "Bearer "+oauth["accessToken"].(string))
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, errOffline
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errRateLimited
	}
	if resp.StatusCode != 200 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error.Message != "" {
			return nil, fmt.Errorf("%s (%d)", strings.TrimSuffix(e.Error.Message, "."), resp.StatusCode)
		}
		return nil, fmt.Errorf("usage request failed (%d)", resp.StatusCode)
	}
	var u usage
	return &u, json.NewDecoder(resp.Body).Decode(&u)
}
