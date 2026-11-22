package station

import "time"

const (
	ScrapePending = "pending"
	ScrapeRunning = "running"
	ScrapeSuccess = "success"
	ScrapeNoMatch = "no_match"
	ScrapeRetry   = "retry"
	ScrapeFailed  = "failed"
	ScrapePaused  = "paused"
)

type ThirdPartyProviderConfig struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	Provider  string    `gorm:"uniqueIndex;size:64" json:"provider"`
	Enabled   bool      `json:"enabled"`
	Paused    bool      `json:"paused"`
	APIKey    string    `gorm:"size:512" json:"-"`
	UpdatedAt time.Time `json:"updatedAt"`

	OAuthConsumerKey       string `gorm:"column:oauth_consumer_key;size:512" json:"-"`
	OAuthConsumerSecret    string `gorm:"column:oauth_consumer_secret;size:512" json:"-"`
	OAuthAccessToken       string `gorm:"column:oauth_access_token;size:512" json:"-"`
	OAuthAccessTokenSecret string `gorm:"column:oauth_access_token_secret;size:512" json:"-"`
}

func (c ThirdPartyProviderConfig) discogsOAuthConnected() bool {
	return c.Provider == discogsProvider && c.OAuthConsumerKey != "" && c.OAuthConsumerSecret != "" &&
		c.OAuthAccessToken != "" && c.OAuthAccessTokenSecret != ""
}

type ScrapeItem struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	ServerFileID        string     `gorm:"uniqueIndex:ux_scrape_file_provider;size:64;index" json:"serverFileId"`
	Provider            string     `gorm:"uniqueIndex:ux_scrape_file_provider;size:64;index" json:"provider"`
	RunID               string     `gorm:"size:64;index" json:"runId,omitempty"`
	ExternalID          string     `gorm:"size:128" json:"externalId,omitempty"`
	RelatedExternalID   string     `gorm:"size:128" json:"relatedExternalId,omitempty"`
	State               string     `gorm:"size:24;index" json:"state"`
	FailureCount        int        `json:"failureCount"`
	MaxFailures         int        `json:"maxFailures"`
	DependencyWaitCount int        `json:"dependencyWaitCount"`
	NextRetryAt         *time.Time `gorm:"index" json:"nextRetryAt,omitempty"`
	LastError           string     `gorm:"size:1024" json:"lastError,omitempty"`
	LastErrorCode       string     `gorm:"size:64" json:"lastErrorCode,omitempty"`
	StartedAt           *time.Time `json:"startedAt,omitempty"`
	FinishedAt          *time.Time `json:"finishedAt,omitempty"`
	LeaseUntil          *time.Time `gorm:"index" json:"leaseUntil,omitempty"`
	AddedCount          int        `json:"addedCount"`
	CandidateVersion    int        `gorm:"default:0" json:"-"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`

	DiscogsMatch *discogsPreview `gorm:"-" json:"discogsMatch,omitempty"`
}

// ScrapeRun preserves the scope of one user-initiated provider scan. Items
// already successful before the run are counted as skipped successes, so a
// run's progress always uses the whole music library as its denominator.
type ScrapeRun struct {
	ID             uint      `gorm:"primaryKey" json:"-"`
	RunID          string    `gorm:"uniqueIndex;size:64" json:"runId"`
	Provider       string    `gorm:"index;size:64" json:"provider"`
	Total          int       `json:"total"`
	SkippedSuccess int       `json:"skippedSuccess"`
	CreatedAt      time.Time `json:"createdAt"`
}

type ScrapeSettings struct {
	Paused                bool `json:"paused"`
	MaxFailures           int  `json:"maxFailures"`
	RequestTimeoutSeconds int  `json:"requestTimeoutSeconds"`
	ItemTimeoutSeconds    int  `json:"itemTimeoutSeconds"`
	RetryDelaySeconds     int  `json:"retryDelaySeconds"`
}

type scrapeSettingsRow struct {
	ID                    uint `gorm:"primaryKey"`
	Paused                bool
	MaxFailures           int
	RequestTimeoutSeconds int
	ItemTimeoutSeconds    int
	RetryDelaySeconds     int
}

func defaultScrapeSettings() ScrapeSettings {
	return ScrapeSettings{MaxFailures: 3, RequestTimeoutSeconds: 15, ItemTimeoutSeconds: 45, RetryDelaySeconds: 60}
}

type ScrapeProviderView struct {
	Provider       string `json:"provider"`
	Enabled        bool   `json:"enabled"`
	Paused         bool   `json:"paused"`
	Configured     bool   `json:"configured"`
	RequiresAPIKey bool   `json:"requiresApiKey"`
	OAuthConnected bool   `json:"oauthConnected,omitempty"`
	RunID          string `json:"runId,omitempty"`
	RunTotal       int    `json:"runTotal,omitempty"`
	SkippedSuccess int    `json:"skippedSuccess,omitempty"`
	SkippedUnready int    `json:"skippedUnready,omitempty"`
}
type ScrapeSettingsView struct {
	Settings  ScrapeSettings       `json:"settings"`
	Providers []ScrapeProviderView `json:"providers"`
}
