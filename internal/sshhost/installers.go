package sshhost

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

const posixInstallerScript = `umask 077
key=
IFS= read -r key || exit 65
[ -n "$key" ] || exit 65
if IFS= read -r extra; then exit 65; fi
sshdir=${HOME:?}/.ssh
auth=$sshdir/authorized_keys
[ ! -L "$sshdir" ] && [ ! -h "$sshdir" ] || exit 73
if [ -e "$sshdir" ]; then [ -d "$sshdir" ] || exit 73; else mkdir -m 700 "$sshdir" || exit 74; fi
chmod 700 "$sshdir" || exit 74
[ ! -L "$auth" ] && [ ! -h "$auth" ] || exit 73
if [ -e "$auth" ]; then [ -f "$auth" ] || exit 73; else : >"$auth" || exit 74; fi
chmod 600 "$auth" || exit 74
if ! LC_ALL=C grep -F -x -q "$key" "$auth"; then
  last=$(tail -c 1 "$auth" 2>/dev/null || :)
  [ -z "$last" ] || printf '\n' >>"$auth" || exit 74
  printf '%s\n' "$key" >>"$auth" || exit 74
fi
chmod 600 "$auth" || exit 74
`

const windowsAdminProbeScript = `$ErrorActionPreference='Stop';$i=[Security.Principal.WindowsIdentity]::GetCurrent();$a=@($i.Groups|Where-Object{$_.Value-eq'S-1-5-32-544'}).Count-gt 0;if($a){[Console]::Out.Write('administrator')}else{[Console]::Out.Write('standard')}`

const windowsInstallerCommon = `$ErrorActionPreference='Stop'
function IsAdmin{$i=[Security.Principal.WindowsIdentity]::GetCurrent();return @($i.Groups|Where-Object{$_.Value-eq'S-1-5-32-544'}).Count-gt 0}
function NoReparse([string]$p,[bool]$d){if([IO.File]::Exists($p)-or[IO.Directory]::Exists($p)){$a=[IO.File]::GetAttributes($p);if(($a-band[IO.FileAttributes]::ReparsePoint)-ne 0){exit 73};if($d-and-not[IO.Directory]::Exists($p)){exit 73};if((-not$d)-and-not[IO.File]::Exists($p)){exit 73}}}
function SetRules([string]$p,$owner,$other,[bool]$d){if($d){$a=[Security.AccessControl.DirectorySecurity]::new();$f=[Security.AccessControl.InheritanceFlags]::ContainerInherit-bor[Security.AccessControl.InheritanceFlags]::ObjectInherit}else{$a=[Security.AccessControl.FileSecurity]::new();$f=[Security.AccessControl.InheritanceFlags]::None};$a.SetAccessRuleProtection($true,$false);$a.SetOwner($owner);$r=[Security.AccessControl.FileSystemRights]::FullControl;$n=[Security.AccessControl.PropagationFlags]::None;$t=[Security.AccessControl.AccessControlType]::Allow;$a.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($owner,$r,$f,$n,$t));$a.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($other,$r,$f,$n,$t));Set-Acl -LiteralPath $p -AclObject $a}
function VerifyRules([string]$p,$one,$two){$a=Get-Acl -LiteralPath $p;if(-not$a.AreAccessRulesProtected){exit 74};$r=@($a.GetAccessRules($true,$false,[Security.Principal.SecurityIdentifier]));if($r.Count-ne 2){exit 74};$seen=@{};foreach($x in $r){if($x.AccessControlType-ne[Security.AccessControl.AccessControlType]::Allow-or$x.FileSystemRights-ne[Security.AccessControl.FileSystemRights]::FullControl){exit 74};$seen[$x.IdentityReference.Value]=$true};if(-not$seen[$one.Value]-or-not$seen[$two.Value]){exit 74}}
$raw=[Console]::In.ReadToEnd();if($raw.Length-lt 2-or$raw.Length-gt 16385-or$raw-notmatch'\A[^\r\n]+\r?\n\z'){exit 65};$key=$raw.TrimEnd([char[]]@(13,10));$utf8=[Text.UTF8Encoding]::new($false)
`

const windowsStandardInstallerBody = `if(IsAdmin){exit 76};$me=[Security.Principal.WindowsIdentity]::GetCurrent().User;$sy=[Security.Principal.SecurityIdentifier]::new('S-1-5-18');$d=Join-Path $env:USERPROFILE '.ssh';$f=Join-Path $d 'authorized_keys';NoReparse $env:USERPROFILE $true;NoReparse $d $true;if(-not[IO.Directory]::Exists($d)){[IO.Directory]::CreateDirectory($d)|Out-Null};SetRules $d $me $sy $true;VerifyRules $d $me $sy;NoReparse $f $false;if([IO.File]::Exists($f)){$b=[IO.File]::ReadAllBytes($f);if([Array]::IndexOf($b,[byte]0)-ge 0){exit 74};$lines=[IO.File]::ReadAllLines($f);$found=$lines-ccontains$key;if(-not$found){$prefix='';if($b.Length-gt 0-and$b[$b.Length-1]-ne 10-and$b[$b.Length-1]-ne 13){$prefix=[Environment]::NewLine};[IO.File]::AppendAllText($f,$prefix+$key+[Environment]::NewLine,$utf8)}}else{[IO.File]::WriteAllText($f,$key+[Environment]::NewLine,$utf8)};NoReparse $f $false;SetRules $f $me $sy $false;VerifyRules $f $me $sy`

const windowsAdminInstallerBody = `if(-not(IsAdmin)){exit 76};$ba=[Security.Principal.SecurityIdentifier]::new('S-1-5-32-544');$sy=[Security.Principal.SecurityIdentifier]::new('S-1-5-18');$d=Join-Path $env:ProgramData 'ssh';$f=Join-Path $d 'administrators_authorized_keys';NoReparse $env:ProgramData $true;NoReparse $d $true;if(-not[IO.Directory]::Exists($d)){[IO.Directory]::CreateDirectory($d)|Out-Null};NoReparse $f $false;if([IO.File]::Exists($f)){$b=[IO.File]::ReadAllBytes($f);if([Array]::IndexOf($b,[byte]0)-ge 0){exit 74};$lines=[IO.File]::ReadAllLines($f);$found=$lines-ccontains$key;if(-not$found){$prefix='';if($b.Length-gt 0-and$b[$b.Length-1]-ne 10-and$b[$b.Length-1]-ne 13){$prefix=[Environment]::NewLine};[IO.File]::AppendAllText($f,$prefix+$key+[Environment]::NewLine,$utf8)}}else{[IO.File]::WriteAllText($f,$key+[Environment]::NewLine,$utf8)};NoReparse $f $false;SetRules $f $ba $sy $false;VerifyRules $f $ba $sy`

func posixInstallerProgram() string {
	return "sh -c " + shellSingleQuote(posixInstallerScript)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func windowsAdminProbeProgram() string {
	return powershellEncodedProgram(windowsAdminProbeScript)
}

func windowsInstallerProgram(administrator bool) string {
	body := windowsStandardInstallerBody
	if administrator {
		body = windowsAdminInstallerBody
	}
	return powershellEncodedProgram(windowsInstallerCommon + body)
}

func powershellEncodedProgram(script string) string {
	units := utf16.Encode([]rune(script))
	bytes := make([]byte, len(units)*2)
	for index, unit := range units {
		bytes[index*2] = byte(unit)
		bytes[index*2+1] = byte(unit >> 8)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(bytes)
}
