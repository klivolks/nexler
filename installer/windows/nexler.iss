; Inno Setup script for the Nexler CLI (Windows, amd64 + arm64).
;
; Built and run end-to-end on the dev machine that wrote this (ISCC.exe,
; via chocolatey) — see BUILDING.md. In CI, .github/workflows/release.yml
; passes the release version via /DVersion=; a plain local ISCC compile
; without that flag falls back to the literal default below.
;
; Payload layout expected before compiling (see BUILDING.md /
; release.yml): payload\amd64\nexler.exe and payload\arm64\nexler.exe,
; both already built with the matching -ldflags -X main.cliVersion=...
; so `nexler version` matches this installer's own AppVersion.

#ifndef Version
  #define Version "0.0.0-dev"
#endif

[Setup]
; Fixed, never-changes-across-releases GUID — Inno Setup uses this to
; recognize "this is the same product" across versions for
; upgrade/uninstall purposes. Generated once for this project.
AppId={{90DFF396-052E-49D2-80EA-8BCD903C991F}}
AppName=Nexler
AppVersion={#Version}
AppPublisher=klivolks
AppPublisherURL=https://github.com/klivolks/nexler
DefaultDirName={autopf}\Nexler
DefaultGroupName=Nexler
; Needed for: writing to the system PATH (HKLM), and silently installing
; the Go/Node MSIs when the user opts in — both are machine-wide changes.
PrivilegesRequired=admin
; x64compatible covers real x64 hardware AND arm64-under-x64-emulation;
; combined with the explicit arm64 alternative below, this correctly
; rejects pure 32-bit x86 hardware, which gets no matching binary.
ArchitecturesAllowed=x64compatible or arm64
ArchitecturesInstallIn64BitMode=x64compatible or arm64
SetupIconFile=nexler.ico
; Shown for the Control Panel / Settings "Apps" list entry — same reason
; the shortcuts below need it too: a plain `go build` binary has no
; embedded icon resource of its own for Windows to fall back to.
UninstallDisplayIcon={app}\nexler.ico
; MIT — permissive enough that just showing it (not requiring a click to
; accept beyond the wizard's own standard Next button) is appropriate;
; Inno still always displays it as its own page either way.
LicenseFile=..\..\LICENSE
WizardStyle=modern
; Sizes match Inno's own recommended dimensions for WizardStyle=modern
; (192x386 for the tall Welcome/Finished-page banner, 76x80 for the small
; corner logo shown on every other page) — both regenerated from
; installer/wizard-banner.svg / wizard-small.svg via build/gen-icons, same
; pipeline as nexler.ico; see BUILDING.md.
WizardImageFile=wizard-image.png
WizardSmallImageFile=wizard-small-image.png
OutputBaseFilename=nexler-setup-{#Version}
OutputDir=.
Compression=lzma2
SolidCompression=yes
DisableProgramGroupPage=yes

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
; Only one of these two actually gets installed on a given machine (see
; Check:) — both land at the same destination filename so the rest of
; the script (shortcuts, PATH) never needs to know which arch is in use.
Source: "payload\arm64\nexler.exe"; DestDir: "{app}"; DestName: "nexler.exe"; Check: IsArm64; Flags: ignoreversion
Source: "payload\amd64\nexler.exe"; DestDir: "{app}"; DestName: "nexler.exe"; Check: (IsX64Compatible and not IsArm64); Flags: ignoreversion
Source: "..\..\README.md"; DestDir: "{app}"; Flags: ignoreversion
; Installed alongside nexler.exe purely so the shortcuts below have an
; icon to point at — a plain `go build` binary has no embedded icon
; resource of its own for IconFilename to extract from.
Source: "nexler.ico"; DestDir: "{app}"; Flags: ignoreversion

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop icon"; GroupDescription: "Additional icons:"; Flags: checkedonce
; These two only appear at all when the corresponding tool is actually
; missing/outdated (see Check: below, and GoOK/NpmOK in [Code]) — nothing
; to show, nothing to ask, when both are already fine.
Name: "installgo"; Description: "Install Go (needed to build apps you scaffold with Nexler)"; GroupDescription: "Optional prerequisites:"; Check: NeedGo
Name: "installnpm"; Description: "Install Node.js/npm (needed for 'nexler add')"; GroupDescription: "Optional prerequisites:"; Check: NeedNpm

[Icons]
Name: "{group}\Nexler Command Prompt"; Filename: "{cmd}"; Parameters: "/K ""cd /d ""{app}"" && nexler.exe help"""; WorkingDir: "{app}"; IconFilename: "{app}\nexler.ico"
Name: "{group}\README"; Filename: "{app}\README.md"
Name: "{group}\Uninstall Nexler"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Nexler"; Filename: "{cmd}"; Parameters: "/K ""cd /d ""{app}"" && nexler.exe help"""; WorkingDir: "{app}"; IconFilename: "{app}\nexler.ico"; Tasks: desktopicon

[Run]
Filename: "{app}\README.md"; Description: "View README"; Flags: postinstall shellexec skipifsilent

[Code]
var
  GoOK, NpmOK: Boolean;
  GoDetected, NpmDetected: String;

// RunCaptured shells cmd via cmd.exe, redirecting combined stdout+stderr
// to a temp file, then returns that file's contents — Inno's Exec has no
// stdout-capture parameter, so this redirect-to-file-then-read pattern is
// the standard workaround. Returns '' (and False) if the process itself
// couldn't be launched at all; a non-zero exit code from cmd still
// returns whatever it printed (e.g. "'go' is not recognized...").
function RunCaptured(const Cmd: String; var Output: String): Boolean;
var
  TmpFile: String;
  ResultCode: Integer;
  RawOutput: AnsiString; // LoadStringFromFile's var param is specifically
                          // AnsiString, not Inno 6's default Unicode String
begin
  TmpFile := ExpandConstant('{tmp}\nexler-check.txt');
  Result := Exec(ExpandConstant('{cmd}'), '/C "' + Cmd + ' > "' + TmpFile + '" 2>&1"', '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if Result and FileExists(TmpFile) then
  begin
    LoadStringFromFile(TmpFile, RawOutput);
    Output := String(RawOutput);
    DeleteFile(TmpFile);
  end
  else
    Output := '';
end;

// ParseGoVersion extracts (major, minor) from a "go version" transcript
// like "go version go1.26.5 windows/amd64". Returns False if no
// "goMAJOR.MINOR" pattern is found at all (e.g. Go isn't installed and
// cmd.exe printed "'go' is not recognized...").
function ParseGoVersion(const Transcript: String; var Major, Minor: Integer): Boolean;
var
  I, J, K, Len: Integer;
  MajorStr, MinorStr: String;
begin
  Result := False;
  Len := Length(Transcript);
  I := Pos('go1', Transcript);
  if I = 0 then Exit;
  I := I + 2; // position at the digit after "go"
  J := I;
  while (J <= Len) and (Transcript[J] <> '.') do J := J + 1;
  if (J > Len) or (J = I) then Exit;
  MajorStr := Copy(Transcript, I, J - I);
  K := J + 1;
  J := K;
  while (J <= Len) and (Transcript[J] >= '0') and (Transcript[J] <= '9') do J := J + 1;
  if J = K then Exit;
  MinorStr := Copy(Transcript, K, J - K);
  Major := StrToIntDef(MajorStr, -1);
  Minor := StrToIntDef(MinorStr, -1);
  Result := (Major >= 0) and (Minor >= 0);
end;

// DetectGo/DetectNpm run once, in InitializeWizard — using an absolute,
// well-known default install path first (Go and Node's official MSIs
// always install to Program Files\Go and Program Files\nodejs
// respectively) since that's reliable even for a tool this exact
// installer run just silently installed a moment ago; a plain PATH-based
// "go version" is the fallback for a non-default install location. This
// matters because a WM_SETTINGCHANGE broadcast only refreshes *other*
// already-running processes' view of PATH, never this installer's own
// frozen environment block — so relying on PATH alone right after a
// same-session install would spuriously report "still missing".
procedure DetectGo;
var
  Transcript: String;
  Major, Minor: Integer;
  GoExe: String;
begin
  GoOK := False;
  GoDetected := 'not found';
  GoExe := ExpandConstant('{pf}\Go\bin\go.exe');
  if FileExists(GoExe) then
    RunCaptured('"' + GoExe + '" version', Transcript)
  else
    RunCaptured('go version', Transcript);
  if ParseGoVersion(Transcript, Major, Minor) then
  begin
    GoDetected := Format('go%d.%d', [Major, Minor]);
    GoOK := (Major > 1) or ((Major = 1) and (Minor >= 26));
  end;
end;

procedure DetectNpm;
var
  Transcript: String;
  NpmCmd: String;
begin
  NpmOK := False;
  NpmDetected := 'not found';
  NpmCmd := ExpandConstant('{pf}\nodejs\npm.cmd');
  if FileExists(NpmCmd) then
    RunCaptured('"' + NpmCmd + '" --version', Transcript)
  else
    RunCaptured('npm --version', Transcript);
  Transcript := Trim(Transcript);
  // A found npm prints a bare version like "10.9.3"; a missing one
  // prints cmd.exe's "'npm' is not recognized..." — the digit/dot check
  // is enough to tell those apart without a full parser.
  if (Transcript <> '') and (Transcript[1] >= '0') and (Transcript[1] <= '9') then
  begin
    NpmOK := True;
    NpmDetected := Transcript;
  end;
end;

procedure InitializeWizard;
begin
  DetectGo;
  DetectNpm;
end;

// Check: (used by [Tasks]) needs a parameterless Boolean function name,
// not a bare variable reference — these just wrap GoOK/NpmOK for that.
function NeedGo: Boolean;
begin
  Result := not GoOK;
end;

function NeedNpm: Boolean;
begin
  Result := not NpmOK;
end;

function SendMessageTimeout(hWnd: LongInt; Msg: LongInt; wParam: LongInt;
  lParam: PAnsiChar; fuFlags: LongInt; uTimeout: LongInt;
  var lpdwResult: LongInt): LongInt;
  external 'SendMessageTimeoutA@user32.dll stdcall';

procedure BroadcastEnvironmentChange;
var
  ResultCode: LongInt;
begin
  SendMessageTimeout($FFFF, $001A, 0, 'Environment', 2, 5000, ResultCode);
end;

// AppendSystemPath adds Dir to the machine-wide PATH (skipping if already
// present) and broadcasts WM_SETTINGCHANGE so new processes pick it up
// without a reboot. Standard "modpath.iss"-style pattern — Inno Setup has
// no built-in [Setup] directive for this.
procedure AppendSystemPath(const Dir: String);
var
  Paths: String;
begin
  if not RegQueryStringValue(HKEY_LOCAL_MACHINE,
    'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Paths) then
    Paths := '';
  if (Paths <> '') and (Pos(';' + Uppercase(Dir) + ';', ';' + Uppercase(Paths) + ';') > 0) then
    Exit; // already present

  if (Paths <> '') and (Paths[Length(Paths)] <> ';') then
    Paths := Paths + ';';
  Paths := Paths + Dir;

  // Must stay REG_EXPAND_SZ, not a plain string — round-tripping through
  // the wrong type would break any existing %VAR%-style entries.
  RegWriteExpandStringValue(HKEY_LOCAL_MACHINE,
    'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Paths);
  BroadcastEnvironmentChange;
end;

procedure RemoveFromSystemPath(const Dir: String);
var
  Paths, NewPaths, Part: String;
  P: Integer;
begin
  if not RegQueryStringValue(HKEY_LOCAL_MACHINE,
    'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Paths) then
    Exit;
  Paths := Paths + ';';
  NewPaths := '';
  while Length(Paths) > 0 do
  begin
    P := Pos(';', Paths);
    Part := Copy(Paths, 1, P - 1);
    Paths := Copy(Paths, P + 1, Length(Paths));
    if (Part <> '') and (Uppercase(Part) <> Uppercase(Dir)) then
    begin
      if NewPaths <> '' then NewPaths := NewPaths + ';';
      NewPaths := NewPaths + Part;
    end;
  end;
  RegWriteExpandStringValue(HKEY_LOCAL_MACHINE,
    'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', NewPaths);
  BroadcastEnvironmentChange;
end;

// DownloadAndInstallMSI curl.exe's the given URL to a temp file, then
// silently msiexec-installs it. Both inherit this installer's own admin
// elevation automatically (Windows doesn't de-elevate child processes),
// so no extra runas handling is needed. curl.exe ships natively in
// System32 on Windows 10 1803+/11 — no bundled download plugin needed.
function DownloadAndInstallMSI(const Url, DisplayName: String): Boolean;
var
  MsiPath: String;
  ResultCode: Integer;
begin
  Result := False;
  MsiPath := ExpandConstant('{tmp}\') + DisplayName + '.msi';
  if not Exec(ExpandConstant('{sys}\curl.exe'), '-L -o "' + MsiPath + '" "' + Url + '"', '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    MsgBox('Downloading ' + DisplayName + ' failed. You can install it yourself later.', mbError, MB_OK);
    Exit;
  end;
  if not Exec('msiexec.exe', '/i "' + MsiPath + '" /qn /norestart', '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    MsgBox('Installing ' + DisplayName + ' failed (exit code ' + IntToStr(ResultCode) +
      '). You can install it yourself later.', mbError, MB_OK);
    Exit;
  end;
  Result := True;
end;

function WindowsArchTag: String;
begin
  if IsArm64 then
    Result := 'arm64'
  else
    Result := 'amd64';
end;

const
  GoVersion = '1.26.0';
  NodeVersion = '22.18.0';

procedure CurStepChanged(CurStep: TSetupStep);
var
  GoUrl, NodeUrl: String;
begin
  if CurStep = ssPostInstall then
  begin
    AppendSystemPath(ExpandConstant('{app}'));

    if WizardIsTaskSelected('installgo') then
    begin
      GoUrl := 'https://go.dev/dl/go' + GoVersion + '.windows-' + WindowsArchTag + '.msi';
      DownloadAndInstallMSI(GoUrl, 'go-setup');
    end;

    if WizardIsTaskSelected('installnpm') then
    begin
      NodeUrl := 'https://nodejs.org/dist/v' + NodeVersion + '/node-v' + NodeVersion + '-' +
        WindowsArchTag + '.msi';
      DownloadAndInstallMSI(NodeUrl, 'node-setup');
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  // Deliberately does NOT remove Go or Node/npm — general-purpose system
  // tools the user may use for other things. Only clean up what this
  // installer itself added to the system PATH.
  if CurUninstallStep = usPostUninstall then
    RemoveFromSystemPath(ExpandConstant('{app}'));
end;
