package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultTokenURL = "https://oauth2.googleapis.com/token"

type authorizedUser struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

type refreshResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

type refreshError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func tokenURL() string {
	if endpoint := os.Getenv("MAILBOX_TOKEN_URL"); endpoint != "" {
		return endpoint
	}
	return defaultTokenURL
}

func refreshAccessToken(ctx context.Context, key, rawJSON string) (accessToken string, expiry time.Time, err error) {
	var credential authorizedUser
	if err := json.Unmarshal([]byte(rawJSON), &credential); err != nil {
		return "", time.Time{}, fmt.Errorf("parse authorized_user JSON from %s: %w", key, err)
	}
	for _, field := range []struct {
		name, value string
	}{
		{name: "client_id", value: credential.ClientID},
		{name: "client_secret", value: credential.ClientSecret},
		{name: "refresh_token", value: credential.RefreshToken},
	} {
		if field.value == "" {
			return "", time.Time{}, fmt.Errorf("authorized_user JSON from %s is missing non-empty %s", key, field.name)
		}
	}

	form := url.Values{
		"client_id":     {credential.ClientID},
		"client_secret": {credential.ClientSecret},
		"refresh_token": {credential.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("oauth refresh for %s: create request: %w", key, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("oauth refresh for %s: %w", key, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		var failure refreshError
		if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
			return "", time.Time{}, fmt.Errorf("oauth refresh for %s failed: %s: decode error response: %w", key, response.Status, err)
		}
		message := failure.Error
		if message == "" {
			message = failure.ErrorDescription
		}
		if message == "" {
			message = "empty error response"
		}
		return "", time.Time{}, fmt.Errorf("oauth refresh for %s failed: %s %s", key, response.Status, message)
	}

	var refreshed refreshResponse
	if err := json.NewDecoder(response.Body).Decode(&refreshed); err != nil {
		return "", time.Time{}, fmt.Errorf("oauth refresh for %s: decode response: %w", key, err)
	}
	if refreshed.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("oauth refresh for %s returned an empty access_token", key)
	}
	if refreshed.ExpiresIn <= 0 {
		return "", time.Time{}, fmt.Errorf("oauth refresh for %s returned invalid expires_in %s", key, strconv.FormatInt(refreshed.ExpiresIn, 10))
	}
	return refreshed.AccessToken, time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second), nil
}
