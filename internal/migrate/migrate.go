package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"context-bridge/internal/store"
)

type manifest struct {
	Version int              `json:"version"`
	Session string           `json:"session"`
	Outputs []manifestOutput `json:"outputs"`
}

type manifestOutput struct {
	Seq            int    `json:"seq"`
	Agent          string `json:"agent"`
	Description    string `json:"description"`
	Preview        string `json:"preview"`
	File           string `json:"file"`
	CallID         string `json:"callID"`
	ChildSessionID string `json:"childSessionID"`
	Timestamp      string `json:"timestamp"`
	Bytes          int    `json:"bytes"`
}

func ImportManifestDir(st *store.Store, sessionsDir string) (int, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0, fmt.Errorf("read sessions dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	imported := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(sessionsDir, entry.Name(), "manifest.json")
		payload, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return imported, err
		}

		var mf manifest
		if err := json.Unmarshal(payload, &mf); err != nil {
			return imported, fmt.Errorf("parse %s: %w", manifestPath, err)
		}
		if strings.TrimSpace(mf.Session) == "" {
			continue
		}

		if err := st.EnsureSession(mf.Session, ""); err != nil {
			return imported, err
		}

		sort.Slice(mf.Outputs, func(i, j int) bool { return mf.Outputs[i].Seq < mf.Outputs[j].Seq })

		for _, output := range mf.Outputs {
			if strings.TrimSpace(output.File) == "" {
				continue
			}

			contentPath := filepath.Join(sessionsDir, entry.Name(), output.File)
			content, err := os.ReadFile(contentPath)
			if err != nil {
				return imported, fmt.Errorf("read %s: %w", contentPath, err)
			}

			capturedAt := time.Time{}
			if strings.TrimSpace(output.Timestamp) != "" {
				if parsed, err := time.Parse(time.RFC3339Nano, output.Timestamp); err == nil {
					capturedAt = parsed.UTC()
				}
			}

			before, err := st.GetCaptureBySeq(mf.Session, output.Seq)
			if err == nil && before != nil && before.CallID == output.CallID {
				continue
			}

			record, err := st.ImportCapture(mf.Session, store.CaptureInput{
				ParentSessionID: mf.Session,
				ChildSessionID:  output.ChildSessionID,
				CallID:          output.CallID,
				Agent:           output.Agent,
				Description:     output.Description,
				Content:         string(content),
				CapturedAt:      capturedAt,
			}, output.Seq, contentPath, output.Preview, output.Bytes, true)
			if err != nil {
				return imported, fmt.Errorf("import %s: %w", contentPath, err)
			}
			if record != nil {
				imported++
			}
		}
	}

	return imported, nil
}
