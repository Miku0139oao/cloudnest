package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// onedriveDriveParams looks up the drive a OneDrive token belongs to.
//
// rclone's config/create only stores what it is given. With a bare token the
// onedrive backend has no drive_id/drive_type and every later call fails with
// "unable to get drive_id and drive_type". rclone's own config wizard fills
// those by asking Microsoft Graph for /me/drive, so Cloudnest does the same.
func onedriveDriveParams(ctx context.Context, token string) (map[string]any, error) {
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(token), &tok); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return nil, fmt.Errorf("token has no access_token")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://graph.microsoft.com/v1.0/me/drive", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graph returned %s", resp.Status)
	}

	var drive struct {
		ID        string `json:"id"`
		DriveType string `json:"driveType"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&drive); err != nil {
		return nil, err
	}
	if drive.ID == "" {
		return nil, fmt.Errorf("graph returned no drive id")
	}
	return map[string]any{
		"drive_id":   drive.ID,
		"drive_type": drive.DriveType,
	}, nil
}
