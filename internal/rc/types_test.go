package rc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestRemoteUnmarshalJSON(t *testing.T) {
	cases := []struct {
		input string
		name  string
		typ   string
	}{
		{`"drive"`, "drive", ""},
		{`{"name":"drive","type":"drive"}`, "drive", "drive"},
		{`{"Name":"dropbox","Type":"dropbox"}`, "dropbox", "dropbox"},
	}
	for _, tc := range cases {
		var got Remote
		if err := json.Unmarshal([]byte(tc.input), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.input, err)
		}
		if got.Name != tc.name || got.Type != tc.typ {
			t.Errorf("unmarshal %s = %+v", tc.input, got)
		}
	}
}

func TestListRemotesDecodesStrings(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"remotes":["drive","dropbox"]}`)
	})
	remotes, err := client.ListRemotes(context.Background(), true)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	if len(remotes) != 2 || remotes[0].Name != "drive" || remotes[1].Name != "dropbox" {
		t.Fatalf("remotes = %+v", remotes)
	}
}

func TestJobListDecodesBothShapes(t *testing.T) {
	var v170 JobList
	if err := json.Unmarshal([]byte(`{"executeId":"x","jobids":[1,2,3]}`), &v170); err != nil {
		t.Fatal(err)
	}
	if len(v170.JobIDs) != 3 || v170.ExecuteID != "x" {
		t.Fatalf("v1.70 job list = %+v", v170)
	}

	var split JobList
	if err := json.Unmarshal([]byte(`{"runningIds":[4],"finishedIds":[5,6]}`), &split); err != nil {
		t.Fatal(err)
	}
	if len(split.RunningIDs) != 1 || len(split.FinishedIDs) != 2 {
		t.Fatalf("split job list = %+v", split)
	}
}

func TestJobStatusDecodesRealResponse(t *testing.T) {
	const payload = `{
		"duration": 0.0113491,
		"endTime": "2026-11-14T08:11:23.8246305+08:00",
		"error": "",
		"finished": true,
		"group": "job/1",
		"id": 1,
		"output": {"free": 123},
		"startTime": "2026-11-14T08:11:23.813281+08:00",
		"success": true
	}`
	var status JobStatus
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Finished || !status.Success || status.ID != 1 || status.Group != "job/1" {
		t.Fatalf("status = %+v", status)
	}
	if status.StartTime.IsZero() || status.EndTime.IsZero() {
		t.Fatalf("times not decoded: %+v", status)
	}
}

func TestStatsDecodesNullETA(t *testing.T) {
	const payload = `{"bytes":6,"eta":null,"fatalError":false,"totalBytes":6,"transferring":[{"name":"a","percentage":50,"eta":null,"srcFs":"src:","dstFs":"dst:"}]}`
	var stats Stats
	if err := json.Unmarshal([]byte(payload), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.ETA != nil {
		t.Fatalf("ETA = %v, want nil", stats.ETA)
	}
	if len(stats.Transferring) != 1 || stats.Transferring[0].ETA != nil {
		t.Fatalf("transferring = %+v", stats.Transferring)
	}
	if stats.Transferring[0].SrcFs != "src:" || stats.Transferring[0].DstFs != "dst:" {
		t.Fatalf("transferring paths = %+v", stats.Transferring[0])
	}
}

func TestTransferredItemDecodesRealResponse(t *testing.T) {
	const payload = `{
		"error": "",
		"name": "five.bin",
		"size": 5000000,
		"bytes": 5000000,
		"checked": false,
		"started_at": "2026-11-14T08:31:06.6145258+08:00",
		"completed_at": "2026-11-14T08:31:11.3174892+08:00",
		"group": "trg3",
		"srcFs": "C:/src",
		"dstFs": "C:/dst"
	}`
	var item TransferredItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatal(err)
	}
	if item.Name != "five.bin" || item.Bytes != 5000000 || item.Checked {
		t.Fatalf("item = %+v", item)
	}
	if item.StartedAt.IsZero() || item.CompletedAt.IsZero() {
		t.Fatalf("timestamps not decoded: %+v", item)
	}
	if item.SrcFs != "C:/src" || item.DstFs != "C:/dst" {
		t.Fatalf("paths = %+v", item)
	}

	// Newer rclone builds may include timestamp in either numeric or string
	// form; it must never make core/transferred undecodable.
	for _, ts := range []string{`123`, `"2026-11-14T08:31:11Z"`, `null`} {
		var withTimestamp TransferredItem
		if err := json.Unmarshal([]byte(`{"timestamp":`+ts+`}`), &withTimestamp); err != nil {
			t.Errorf("timestamp %s: %v", ts, err)
		}
	}
}

func TestListOptJSONFieldNames(t *testing.T) {
	body, err := json.Marshal(ListOpt{Recurse: true, ShowHash: true, HashTypes: []string{"sha256"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["recurse"] != true || got["showHash"] != true {
		t.Fatalf("ListOpt JSON = %s", body)
	}
	if _, ok := got["showEncrypted"]; ok {
		t.Fatalf("zero ListOpt field was serialized: %s", body)
	}
}

func TestEntryModTime(t *testing.T) {
	if _, err := time.Parse(time.RFC3339Nano, "2026-11-14T08:09:38.119356+08:00"); err != nil {
		t.Fatal(err)
	}
	var entry Entry
	const payload = `{"Path":"a","Name":"a","Size":6,"ModTime":"2026-11-14T08:09:38.119356+08:00","IsDir":false,"MimeType":"text/plain"}`
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Size != 6 || entry.MimeType != "text/plain" || entry.IsDir {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestUsageAndProviderDecode(t *testing.T) {
	var usage Usage
	if err := json.Unmarshal([]byte(`{"free":1,"total":2,"used":1}`), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.Free != 1 || usage.Total != 2 || usage.Used != 1 {
		t.Fatalf("usage = %+v", usage)
	}

	var provider Provider
	const payload = `{"Name":"drive","Description":"Google Drive","Prefix":"drive","Options":[{"Name":"scope","Type":"string","Default":"drive","Required":false,"IsPassword":false,"Advanced":true,"Hide":0,"Provider":"","Sensitive":false,"Examples":[{"Value":"drive","Help":"Full access"}]}]}`
	if err := json.Unmarshal([]byte(payload), &provider); err != nil {
		t.Fatal(err)
	}
	if provider.Name != "drive" || len(provider.Options) != 1 || provider.Options[0].Examples[0].Value != "drive" {
		t.Fatalf("provider = %+v", provider)
	}
}
