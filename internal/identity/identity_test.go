package identity

import (
	"context"
	"errors"
	"testing"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
)

const (
	pnNum  = "6285324023198"
	lidNum = "221203734204662"
	botPN  = "628989240628"
	botLID = "111222333444555"
)

// fakeLID adalah LIDStore in-memory untuk tes resolve LID↔PN tanpa DB.
type fakeLID struct {
	byLID map[string]string // lid user -> pn user
	byPN  map[string]string // pn user -> lid user
}

func (f fakeLID) PutManyLIDMappings(context.Context, []store.LIDMapping) error { return nil }
func (f fakeLID) PutLIDMapping(context.Context, types.JID, types.JID) error    { return nil }
func (f fakeLID) GetManyLIDsForPNs(context.Context, []types.JID) (map[types.JID]types.JID, error) {
	return nil, nil
}
func (f fakeLID) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	if pn, ok := f.byLID[lid.User]; ok {
		return types.NewJID(pn, types.DefaultUserServer), nil
	}
	return types.JID{}, errors.New("no mapping")
}
func (f fakeLID) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if lid, ok := f.byPN[pn.User]; ok {
		return types.NewJID(lid, types.HiddenUserServer), nil
	}
	return types.JID{}, errors.New("no mapping")
}

func newClient(lids store.LIDStore, id *types.JID, botLID types.JID) *whatsmeow.Client {
	return &whatsmeow.Client{Store: &store.Device{ID: id, LID: botLID, LIDs: lids}}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   types.JID
		want types.JID
	}{
		{"hosted → PN", types.NewJID("123", types.HostedServer), types.NewJID("123", types.DefaultUserServer)},
		{"hosted.lid → LID", types.NewJID("456", types.HostedLIDServer), types.NewJID("456", types.HiddenUserServer)},
		{"AD dibuang", types.NewADJID(pnNum, 0, 5), types.NewJID(pnNum, types.DefaultUserServer)},
		{"grup tetap", types.NewJID("123", types.GroupServer), types.NewJID("123", types.GroupServer)},
	}
	for _, tc := range cases {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("%s: Normalize(%s) = %s, mau %s", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSameUser(t *testing.T) {
	if !SameUser(types.NewADJID(pnNum, 0, 3), types.NewJID(pnNum, types.DefaultUserServer)) {
		t.Error("PN + device harus user-sama")
	}
	if SameUser(types.NewJID(pnNum, types.DefaultUserServer), types.NewJID(lidNum, types.HiddenUserServer)) {
		t.Error("PN vs LID TIDAK boleh user-sama (butuh alt/resolve)")
	}
	if SameUser(types.JID{}, types.NewJID(pnNum, types.DefaultUserServer)) {
		t.Error("JID kosong tidak boleh cocok")
	}
}

func TestCandidateMatches(t *testing.T) {
	// Pengirim LID dengan alt PN.
	c := Candidate{
		JID: types.NewJID(lidNum, types.HiddenUserServer),
		Alt: types.NewJID(pnNum, types.DefaultUserServer),
	}
	if !c.Matches(types.NewJID(lidNum, types.HiddenUserServer)) {
		t.Error("harus cocok lewat JID LID")
	}
	if !c.Matches(types.NewJID(pnNum, types.DefaultUserServer)) {
		t.Error("harus cocok lewat Alt PN")
	}
	if !c.MatchesAny(types.NewJID("9", types.DefaultUserServer), types.NewJID(pnNum, types.DefaultUserServer)) {
		t.Error("MatchesAny harus menemukan salah satu")
	}
	if c.Matches(types.NewJID("999", types.DefaultUserServer)) {
		t.Error("tidak boleh cocok dengan user lain")
	}
}

func TestResolvePN(t *testing.T) {
	ctx := context.Background()
	client := newClient(fakeLID{
		byLID: map[string]string{lidNum: pnNum},
		byPN:  map[string]string{pnNum: lidNum},
	}, ptr(types.NewJID(botPN, types.DefaultUserServer)), types.NewJID(botLID, types.HiddenUserServer))

	// JID sudah PN.
	if pn, ok := ResolvePN(ctx, nil, types.NewJID(pnNum, types.DefaultUserServer), types.JID{}); !ok || pn.User != pnNum {
		t.Errorf("PN langsung: got %v ok=%v", pn, ok)
	}
	// Lewat alt PN (tanpa client).
	if pn, ok := ResolvePN(ctx, nil, types.NewJID(lidNum, types.HiddenUserServer), types.NewJID(pnNum, types.DefaultUserServer)); !ok || pn.User != pnNum {
		t.Errorf("lewat alt: got %v ok=%v", pn, ok)
	}
	// Lewat peta LID↔PN.
	if pn, ok := ResolvePN(ctx, client, types.NewJID(lidNum, types.HiddenUserServer), types.JID{}); !ok || pn.User != pnNum {
		t.Errorf("lewat peta: got %v ok=%v", pn, ok)
	}
	// Tak diketahui + tanpa client → fail-closed.
	if _, ok := ResolvePN(ctx, nil, types.NewJID(lidNum, types.HiddenUserServer), types.JID{}); ok {
		t.Error("tanpa client + tak ada alt harus ok=false")
	}
}

func TestResolveLID(t *testing.T) {
	ctx := context.Background()
	client := newClient(fakeLID{
		byLID: map[string]string{lidNum: pnNum},
		byPN:  map[string]string{pnNum: lidNum},
	}, nil, types.JID{})

	if lid, ok := ResolveLID(ctx, client, types.NewJID(pnNum, types.DefaultUserServer), types.JID{}); !ok || lid.User != lidNum {
		t.Errorf("PN→LID lewat peta: got %v ok=%v", lid, ok)
	}
	if lid, ok := ResolveLID(ctx, nil, types.NewJID(lidNum, types.HiddenUserServer), types.JID{}); !ok || lid.User != lidNum {
		t.Errorf("LID langsung: got %v ok=%v", lid, ok)
	}
}

func TestCanonicalUser(t *testing.T) {
	ctx := context.Background()
	client := newClient(fakeLID{
		byLID: map[string]string{lidNum: pnNum},
		byPN:  map[string]string{pnNum: lidNum},
	}, nil, types.JID{})

	if got := CanonicalUser(ctx, client, types.NewJID(lidNum, types.HiddenUserServer), types.JID{}); got != pnNum {
		t.Errorf("kunci kanonik LID harus resolve ke PN %s, got %s", pnNum, got)
	}
	// Tak dikenal → fallback user apa adanya.
	if got := CanonicalUser(ctx, nil, types.NewJID(lidNum, types.HiddenUserServer), types.JID{}); got != lidNum {
		t.Errorf("fallback harus user LID, got %s", got)
	}
}

func TestKeys(t *testing.T) {
	ctx := context.Background()
	client := newClient(fakeLID{
		byLID: map[string]string{lidNum: pnNum},
		byPN:  map[string]string{pnNum: lidNum},
	}, nil, types.JID{})

	got := Keys(ctx, client, types.NewJID(lidNum, types.HiddenUserServer), types.JID{})
	want := map[string]bool{lidNum: true, pnNum: true}
	if len(got) != len(want) {
		t.Fatalf("Keys = %v, mau %d entri unik", got, len(want))
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("key tak terduga: %s", k)
		}
	}
}

func TestStrings(t *testing.T) {
	ctx := context.Background()
	client := newClient(fakeLID{
		byLID: map[string]string{lidNum: pnNum},
		byPN:  map[string]string{pnNum: lidNum},
	}, nil, types.JID{})

	got := Strings(ctx, client, types.NewJID(lidNum, types.HiddenUserServer), types.JID{})
	want := map[string]bool{
		lidNum + "@lid":           true,
		pnNum + "@s.whatsapp.net": true,
	}
	if len(got) != len(want) {
		t.Fatalf("Strings = %v, mau %d entri unik", got, len(want))
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("string tak terduga: %s", s)
		}
	}
}

func TestIsBot(t *testing.T) {
	client := newClient(fakeLID{}, ptr(types.NewJID(botPN, types.DefaultUserServer)), types.NewJID(botLID, types.HiddenUserServer))

	if !IsBot(client, types.NewJID(botPN, types.DefaultUserServer)) {
		t.Error("PN bot harus dikenali")
	}
	if !IsBot(client, types.NewJID(botLID, types.HiddenUserServer)) {
		t.Error("LID bot harus dikenali")
	}
	if IsBot(client, types.NewJID(pnNum, types.DefaultUserServer)) {
		t.Error("user lain tidak boleh dianggap bot")
	}
	if IsBot(nil, types.NewJID(botPN, types.DefaultUserServer)) {
		t.Error("client nil harus false, tanpa panic")
	}
}

func ptr(j types.JID) *types.JID { return &j }
