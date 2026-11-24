package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAbsensiDBWITHOUTROWID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absensi.db")
	if err := InitAbsensiDB(path); err != nil {
		t.Fatalf("InitAbsensiDB: %v", err)
	}
	defer Absensi().db.Close()

	a := Absensi()
	chat := "120363000000000000@g.us"

	// 1. Config (WITHOUT ROWID)
	if err := a.SetConfig(chat, -6.2088, 106.8456, "Kantor Pusat"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := a.SetRadius(chat, 50.0); err != nil {
		t.Fatalf("SetRadius: %v", err)
	}
	if err := a.SetDefaultDurasi(chat, 120); err != nil {
		t.Fatalf("SetDefaultDurasi: %v", err)
	}
	cfg, err := a.GetConfig(chat)
	if err != nil || cfg == nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Name != "Kantor Pusat" || cfg.RadiusM != 50.0 || cfg.DefaultDurasi != 120 {
		t.Fatalf("unexpected config values: %+v", cfg)
	}

	// 2. Session
	now := time.Now().Unix()
	sessID, err := a.OpenSession(chat, 60, false, "owner", now)
	if err != nil || sessID <= 0 {
		t.Fatalf("OpenSession: %v (id=%d)", err, sessID)
	}
	active, err := a.ActiveSession(chat)
	if err != nil || active == nil || active.ID != sessID {
		t.Fatalf("ActiveSession: %v (%+v)", err, active)
	}

	// 3. Records (WITHOUT ROWID)
	sender := "628123456789@s.whatsapp.net"
	inserted, err := a.AddRecord(sessID, sender, "Budi", now+10, -6.2088, 106.8456, 5.0, 10.0)
	if err != nil || !inserted {
		t.Fatalf("AddRecord: %v (inserted=%v)", err, inserted)
	}
	// Duplicate record in same session -> ignored
	inserted2, err := a.AddRecord(sessID, sender, "Budi", now+20, -6.2088, 106.8456, 5.0, 10.0)
	if err != nil || inserted2 {
		t.Fatalf("expected duplicate record to return inserted=false, got %v (%v)", inserted2, err)
	}

	recs, err := a.ListRecords(sessID)
	if err != nil || len(recs) != 1 {
		t.Fatalf("ListRecords: %v (count=%d)", err, len(recs))
	}

	// 4. Schedules (WITHOUT ROWID)
	if err := a.SetSchedule(chat, "1,2,3,4,5", 480, 540); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if err := a.SetScheduleEnabled(chat, true); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}
	sched, err := a.GetSchedule(chat)
	if err != nil || sched == nil || !sched.Enabled {
		t.Fatalf("GetSchedule: %v (%+v)", err, sched)
	}
}
