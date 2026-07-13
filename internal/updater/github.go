package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultGitHubAPI = "https://api.github.com"
	maxReleaseBody   = int64(2 << 20)
)

type ReleaseSource interface {
	Latest(context.Context, bool) (Release, error)
}

type GitHubClientOptions struct {
	HTTPClient *http.Client
	BaseURL    string
	CacheTTL   time.Duration
	Now        func() time.Time
}

type GitHubClient struct {
	httpClient *http.Client
	baseURL    string
	cacheTTL   time.Duration
	now        func() time.Time

	mu      sync.Mutex
	etag    string
	cached  Release
	hasData bool
}

func NewGitHubClient(options GitHubClientOptions) *GitHubClient {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultGitHubAPI
	}
	cacheTTL := options.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 6 * time.Hour
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &GitHubClient{httpClient: client, baseURL: baseURL, cacheTTL: cacheTTL, now: now}
}

func (c *GitHubClient) Latest(ctx context.Context, force bool) (Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	if !force && c.hasData && now.Sub(c.cached.CheckedAt) < c.cacheTTL {
		return c.cached, nil
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.baseURL, url.PathEscape(RepositoryOwner), url.PathEscape(RepositoryName))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "MailManager-update-checker")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.etag != "" {
		request.Header.Set("If-None-Match", c.etag)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("query GitHub release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		if !c.hasData {
			return Release{}, errors.New("GitHub returned not modified without a cached release")
		}
		c.cached.CheckedAt = now
		return c.cached, nil
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return Release{}, fmt.Errorf("query GitHub release: status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseBody+1))
	if err != nil {
		return Release{}, fmt.Errorf("read GitHub release: %w", err)
	}
	if int64(len(body)) > maxReleaseBody {
		return Release{}, errors.New("GitHub release response is too large")
	}
	var payload struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Release{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	if payload.Draft || payload.Prerelease {
		return Release{}, errors.New("latest GitHub release is not a stable release")
	}
	version, err := NormalizeVersion(payload.TagName)
	if err != nil {
		return Release{}, fmt.Errorf("latest GitHub release tag %q is not stable SemVer: %w", payload.TagName, err)
	}
	publishedAt, err := time.Parse(time.RFC3339, payload.PublishedAt)
	if err != nil {
		return Release{}, fmt.Errorf("parse GitHub release time: %w", err)
	}
	assets := make(map[string]Asset, len(payload.Assets))
	for _, item := range payload.Assets {
		if item.Name != "" && item.URL != "" {
			assets[item.Name] = Asset{Name: item.Name, URL: item.URL, Size: item.Size}
		}
	}
	release := Release{
		Version: version, TagName: payload.TagName, Name: payload.Name, PublishedAt: publishedAt,
		ReleaseNotes: payload.Body, HTMLURL: payload.HTMLURL, CheckedAt: now, Assets: assets,
	}
	c.cached, c.hasData = release, true
	c.etag = response.Header.Get("ETag")
	return release, nil
}
