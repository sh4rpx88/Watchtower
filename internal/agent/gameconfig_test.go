package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

const sampleINI = `# Players can hurt and kill other players
PVP=true

# Ping limit, in milliseconds, before a player is kicked from the server. Minimum=100 Maximum=2147483647 Default=400
PingLimit=400

# Clients may join without already having an account in the whitelist.
Password=1234

# Remote players on the map. 1=Hidden 2=Friends 3=Everyone
# Minimum=1 Maximum=3 Default=1
MapRemotePlayerVisibility=1

# Minimum=0.00 Maximum=10.00 Default=0.50
CarEngineAttractionModifier=0.5

DefaultPort=16261
`

const sampleLua = `SandboxVars = {
    VERSION = 5,
    -- Changing this also sets the "Population Multiplier" in Advanced Zombie Options. Default = Normal
    -- 1 = Insane
    -- 2 = Very High
    -- 3 = High
    -- 4 = Normal
    -- 5 = Low
    -- 6 = None
    Zombies = 4,
    -- Minimum=0.00 Maximum=1000.00 Default=1.00
    XpMultiplier = 1.0,
    -- Comma-separated item types.
    WorldItemRemovalList = "Base.Hat,Base.Glasses",
    BoneFracture = true,
    ZombieLore = {
        -- Default=Fast Shamblers
        -- 1 = Sprinters
        -- 2 = Fast Shamblers
        -- 3 = Shamblers
        Speed = 2,
    },
    ZombieConfig = {
        -- Minimum=0.00 Maximum=4.00 Default=1.00
        PopulationMultiplier = 1.0,
    },
}
`

func findSetting(t *testing.T, doc *gameConfigDoc, key string) protocol.GameSetting {
	t.Helper()
	for _, s := range doc.settings {
		if s.Key == key {
			return s.GameSetting
		}
	}
	t.Fatalf("no setting %s", key)
	return protocol.GameSetting{}
}

func TestParseINI(t *testing.T) {
	doc := parseGameConfig("ini", sampleINI)
	if s := findSetting(t, doc, "PVP"); s.Type != "bool" || s.Description != "Players can hurt and kill other players" {
		t.Errorf("PVP = %+v", s)
	}
	s := findSetting(t, doc, "PingLimit")
	if s.Type != "int" || *s.Min != 100 || *s.Max != 2147483647 || s.Default != "400" || !strings.HasPrefix(s.Description, "Ping limit") {
		t.Errorf("PingLimit = %+v", s)
	}
	if s := findSetting(t, doc, "Password"); s.Type != "string" {
		t.Errorf("a digit password should stay text, got %s", s.Type)
	}
	if s := findSetting(t, doc, "MapRemotePlayerVisibility"); s.Type != "select" || len(s.Choices) != 3 || s.Choices[2].Label != "Everyone" {
		t.Errorf("MapRemotePlayerVisibility = %+v", s)
	}
	if s := findSetting(t, doc, "CarEngineAttractionModifier"); s.Type != "number" {
		t.Errorf("CarEngineAttractionModifier type %s", s.Type)
	}
	if err := doc.set(map[string]string{"PingLimit": "50"}, nil); err == nil {
		t.Error("value below the minimum was accepted")
	}
	if err := doc.set(map[string]string{"Nope": "1"}, nil); err == nil {
		t.Error("unknown key was accepted")
	}
	if err := doc.set(map[string]string{"DefaultPort": "1"}, map[string]string{"DefaultPort": "managed"}); err == nil {
		t.Error("locked key was accepted")
	}
	if err := doc.set(map[string]string{"PVP": "False", "PingLimit": "500", "Password": "hunter 2"}, nil); err != nil {
		t.Fatal(err)
	}
	out := doc.String()
	for _, want := range []string{"\nPVP=false\n", "\nPingLimit=500\n", "\nPassword=hunter 2\n", "# Players can hurt"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestParseLua(t *testing.T) {
	doc := parseGameConfig("lua", sampleLua)
	z := findSetting(t, doc, "Zombies")
	if z.Type != "select" || len(z.Choices) != 6 || z.Choices[0].Label != "Insane" || z.Default != "Normal" {
		t.Errorf("Zombies = %+v", z)
	}
	if !strings.Contains(z.Description, "Population Multiplier") {
		t.Errorf("Zombies description %q", z.Description)
	}
	if s := findSetting(t, doc, "ZombieLore.Speed"); s.Type != "select" || s.Value != "2" {
		t.Errorf("Speed = %+v", s)
	}
	if s := findSetting(t, doc, "XpMultiplier"); s.Type != "number" || *s.Max != 1000 {
		t.Errorf("XpMultiplier = %+v", s)
	}
	if s := findSetting(t, doc, "WorldItemRemovalList"); s.Type != "string" || s.Value != "Base.Hat,Base.Glasses" {
		t.Errorf("WorldItemRemovalList = %+v", s)
	}
	if s := findSetting(t, doc, "VERSION"); s.Type != "int" {
		t.Errorf("VERSION = %+v", s)
	}
	if err := doc.set(map[string]string{"ZombieLore.Speed": "7"}, nil); err == nil {
		t.Error("value outside the choices was accepted")
	}
	err := doc.set(map[string]string{"ZombieLore.Speed": "1", "Zombies": "2", "WorldItemRemovalList": `Base."Odd"`, "ZombieConfig.PopulationMultiplier": "2.5", "BoneFracture": "false"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := doc.String()
	for _, want := range []string{"\n        Speed = 1,\n", "\n    Zombies = 2,\n", `WorldItemRemovalList = "Base.\"Odd\"",`, "PopulationMultiplier = 2.5,", "BoneFracture = false,", "    -- 1 = Insane\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Reading it back gives the same values.
	again := parseGameConfig("lua", out)
	if s := findSetting(t, again, "WorldItemRemovalList"); s.Value != `Base."Odd"` {
		t.Errorf("round trip gave %q", s.Value)
	}

	gc := templates.GameConfig{Groups: []templates.ConfigGroup{{Name: "Population", Match: "^Zombies$"}}, Tables: map[string]string{"ZombieLore": "Zombies"}}
	var names []string
	for _, g := range again.sections(gc, nil) {
		names = append(names, g.Name)
	}
	if got := strings.Join(names, ","); got != "Population,Zombies,Zombie config,Other settings" {
		t.Errorf("sections = %s", got)
	}
}

func TestGameConfigFiles(t *testing.T) {
	m := newTestManager(t)
	spec := protocol.InstanceSpec{ID: testID}
	spec.Settings = map[string]string{"servername": "pz", "PublicName": "Mine"}
	spec.Template.Config = []templates.ConfigField{{Key: "PublicName", Label: "Server name", File: "Zomboid/Server/{{.servername}}.ini"}}
	spec.Template.GameConfig = []templates.GameConfig{{Label: "Server", Path: "Zomboid/Server/{{.servername}}.ini", Format: "ini", Locked: map[string]string{"DefaultPort": "set by Watchtower"}}}
	if err := os.MkdirAll(m.dir(spec.ID), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := m.ReadGameConfig(spec)
	if err != nil || len(files) != 1 || files[0].Exists || files[0].Path != "Zomboid/Server/pz.ini" {
		t.Fatalf("before first start: %+v, %v", files, err)
	}
	if err := m.WriteGameConfig(protocol.GameConfigRequest{InstanceSpec: spec, Values: map[string]string{"PVP": "false"}}); err == nil {
		t.Error("writing a file the game hasn't made yet should fail")
	}

	p := filepath.Join(m.dir(spec.ID), "Zomboid", "Server", "pz.ini")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(sampleINI+"PublicName=Mine\n"), 0o644)
	files, err = m.ReadGameConfig(spec)
	if err != nil || !files[0].Exists {
		t.Fatalf("after: %+v, %v", files, err)
	}
	locked := map[string]string{}
	for _, sec := range files[0].Sections {
		for _, s := range sec.Settings {
			locked[s.Key] = s.Locked
		}
	}
	if locked["PublicName"] == "" || locked["DefaultPort"] == "" || locked["PVP"] != "" {
		t.Errorf("locked = %v", locked)
	}
	if err := m.WriteGameConfig(protocol.GameConfigRequest{InstanceSpec: spec, Values: map[string]string{"PublicName": "x"}}); err == nil {
		t.Error("a setting Setup manages was changed")
	}
	if err := m.WriteGameConfig(protocol.GameConfigRequest{InstanceSpec: spec, Values: map[string]string{"PVP": "false"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "\nPVP=false\n") {
		t.Errorf("file not updated:\n%s", data)
	}
	if err := m.WriteGameConfig(protocol.GameConfigRequest{InstanceSpec: spec, File: 3}); err == nil {
		t.Error("bad file index accepted")
	}
}

func TestSteamcmdBranch(t *testing.T) {
	spec := protocol.InstanceSpec{ID: testID}
	spec.Template.Install = templates.Install{Method: "steamcmd", SteamAppID: 380870, SteamBeta: "{{.branch}}"}
	for branch, want := range map[string]string{"public": "", "": "", "unstable": "unstable", "legacy41": "legacy41"} {
		spec.Settings = map[string]string{"branch": branch}
		args, err := steamcmdArgs(spec, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got := steamBeta(args); got != want {
			t.Errorf("branch %q: got -beta %q", branch, got)
		}
	}
	spec.Settings = map[string]string{"branch": "x; rm -rf /"}
	if _, err := steamcmdArgs(spec, t.TempDir()); err == nil {
		t.Error("bad branch accepted")
	}
}
