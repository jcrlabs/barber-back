package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// Profile es la identidad normalizada que devuelve cualquier proveedor social.
type Profile struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
}

func (p *Profile) DisplayName() string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	if i := strings.IndexByte(p.Email, '@'); i > 0 {
		return p.Email[:i]
	}
	return "Cliente"
}

type OAuthProvider struct {
	Name    string
	Label   string
	config  oauth2.Config
	profile func(ctx context.Context, c *http.Client) (*Profile, error)
}

// AuthCodeURL genera la URL de autorización con state y PKCE (S256).
func (p *OAuthProvider) AuthCodeURL(state, verifier string) string {
	return p.config.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier))
}

func (p *OAuthProvider) Exchange(ctx context.Context, code, verifier string) (*Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := p.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("oauth exchange: %w", err)
	}
	prof, err := p.profile(ctx, p.config.Client(ctx, tok))
	if err != nil {
		return nil, err
	}
	prof.Provider = p.Name
	return prof, nil
}

type OAuthRegistry struct {
	providers map[string]*OAuthProvider
	order     []string
}

func (r *OAuthRegistry) Get(name string) (*OAuthProvider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

type ProviderInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

func (r *OAuthRegistry) List() []ProviderInfo {
	out := []ProviderInfo{}
	for _, n := range r.order {
		out = append(out, ProviderInfo{Name: n, Label: r.providers[n].Label})
	}
	return out
}

type OAuthCredentials struct{ ClientID, ClientSecret string }

// NewOAuthRegistry registra solo los proveedores con credenciales configuradas.
func NewOAuthRegistry(publicURL string, google, github OAuthCredentials) *OAuthRegistry {
	r := &OAuthRegistry{providers: map[string]*OAuthProvider{}}
	redirect := func(name string) string { return publicURL + "/api/v1/auth/oauth/" + name + "/callback" }

	if google.ClientID != "" && google.ClientSecret != "" {
		r.add(&OAuthProvider{
			Name: "google", Label: "Google",
			config: oauth2.Config{
				ClientID: google.ClientID, ClientSecret: google.ClientSecret,
				Endpoint: endpoints.Google, RedirectURL: redirect("google"),
				Scopes: []string{"openid", "email", "profile"},
			},
			profile: googleProfile,
		})
	}
	if github.ClientID != "" && github.ClientSecret != "" {
		r.add(&OAuthProvider{
			Name: "github", Label: "GitHub",
			config: oauth2.Config{
				ClientID: github.ClientID, ClientSecret: github.ClientSecret,
				Endpoint: endpoints.GitHub, RedirectURL: redirect("github"),
				Scopes: []string{"read:user", "user:email"},
			},
			profile: githubProfile,
		})
	}
	return r
}

func (r *OAuthRegistry) add(p *OAuthProvider) {
	r.providers[p.Name] = p
	r.order = append(r.order, p.Name)
}

func getJSON(ctx context.Context, c *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst)
}

func googleProfile(ctx context.Context, c *http.Client) (*Profile, error) {
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := getJSON(ctx, c, "https://openidconnect.googleapis.com/v1/userinfo", &info); err != nil {
		return nil, err
	}
	if info.Sub == "" {
		return nil, errors.New("google: empty subject")
	}
	return &Profile{Subject: info.Sub, Email: info.Email, EmailVerified: info.EmailVerified, Name: info.Name, AvatarURL: info.Picture}, nil
}

func githubProfile(ctx context.Context, c *http.Client) (*Profile, error) {
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, c, "https://api.github.com/user", &user); err != nil {
		return nil, err
	}
	if user.ID == 0 {
		return nil, errors.New("github: empty id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, c, "https://api.github.com/user/emails", &emails); err != nil {
		return nil, err
	}
	p := &Profile{Subject: strconv.FormatInt(user.ID, 10), Name: user.Name, AvatarURL: user.AvatarURL}
	if p.Name == "" {
		p.Name = user.Login
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			p.Email, p.EmailVerified = e.Email, true
			break
		}
	}
	return p, nil
}
