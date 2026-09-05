<#
.SYNOPSIS
  Generate the cosign release-signing keypair for audiobookrenamer's in-app
  self-update, install the public half into the repo, and push the private half
  + passphrase as GitHub Actions secrets.

.DESCRIPTION
  Run this once (and again whenever you rotate the key). It is interactive:
  run it from a real PowerShell terminal, not a non-interactive task runner.

  The in-app updater (internal/selfupdate) refuses any release whose
  checksums.txt does not verify against internal/selfupdate/keys/cosign.pub,
  so this keypair is what lets the web-UI updater trust your releases.

  Requires: cosign, gh (authenticated as the repo owner), git.

.EXAMPLE
  pwsh -File scripts/setup-signing-key.ps1
#>
[CmdletBinding()]
param(
  [string] $Repo = 'FlightlessWeasel/audiobookrenamer'
)

$ErrorActionPreference = 'Stop'

function Info  { param($m) Write-Host "==> $m" -ForegroundColor Cyan }
function Ok    { param($m) Write-Host "  + $m" -ForegroundColor Green }
function Warn  { param($m) Write-Host "  ! $m" -ForegroundColor Yellow }
function Fail  { param($m) Write-Host "error: $m" -ForegroundColor Red; exit 1 }
function Confirm { param($q) ($(Read-Host "$q [y/N]") -match '^[Yy]') }

$repoRoot   = (git rev-parse --show-toplevel).Trim()
$pubKeyDest = Join-Path $repoRoot 'internal/selfupdate/keys/cosign.pub'
$workDir    = Join-Path ([System.IO.Path]::GetTempPath()) ("abr-cosign-" + [guid]::NewGuid().ToString('N').Substring(0,8))
$keyFile    = Join-Path $workDir 'cosign.key'
$pubFile    = Join-Path $workDir 'cosign.pub'

# Resolve a cosign command. The winget package (Sigstore.Cosign) installs the
# binary under the alias "cosign-windows-amd64", not "cosign", so try both.
function Resolve-Cosign {
  foreach ($n in 'cosign', 'cosign-windows-amd64', 'cosign-windows-arm64') {
    $c = Get-Command $n -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
  }
  return $null
}

New-Item -ItemType Directory -Path $workDir -Force | Out-Null
try {
  # ── 1. Preflight ─────────────────────────────────────────────────────────
  Info 'Preflight: cosign + gh + git'

  $Cosign = Resolve-Cosign
  if (-not $Cosign) {
    Warn 'cosign is not installed.'
    if (Confirm 'Install it now with "winget install Sigstore.Cosign"?') {
      winget install --exact --id Sigstore.Cosign --accept-source-agreements --accept-package-agreements
      $env:PATH = [Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('PATH', 'User')
      $Cosign = Resolve-Cosign
      if (-not $Cosign) {
        Fail 'cosign installed but not resolvable in this session. Open a new terminal and re-run.'
      }
    } else {
      Fail 'cosign is required. Install it and re-run.'
    }
  }
  Ok ("cosign: " + ((& $Cosign version 2>$null | Select-Object -First 1)))

  if (-not (Get-Command gh -ErrorAction SilentlyContinue)) { Fail 'gh (GitHub CLI) is not installed.' }
  gh auth status 2>$null | Out-Null
  if ($LASTEXITCODE -ne 0) { Fail 'gh is not authenticated. Run "gh auth login" as the repo owner, then re-run.' }
  $ghUser = (gh api user -q .login 2>$null)
  Ok "gh: authenticated as $ghUser"
  Ok "secrets target repo: $Repo"
  Warn 'Confirm that login owns this repo (commits here are pinned to FlightlessWeasel).'
  if (-not (Confirm 'Continue?')) { Fail 'aborted.' }

  # ── 2. Passphrase ────────────────────────────────────────────────────────
  Info 'Choose a signing passphrase'
  Write-Host '  cosign encrypts the private key with a passphrase. CI needs both the'
  Write-Host '  encrypted key (COSIGN_PRIVATE_KEY) and this passphrase (COSIGN_PASSWORD).'
  Write-Host '  Store it in your password manager now - this script never writes it to disk.'
  $p1 = Read-Host 'New signing passphrase' -AsSecureString
  $p2 = Read-Host 'Repeat it'              -AsSecureString
  $s1 = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($p1))
  $s2 = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($p2))
  if ([string]::IsNullOrEmpty($s1) -or $s1 -ne $s2) { Fail 'passphrases empty or did not match.' }

  # ── 3. Generate the keypair ─────────────────────────────────────────────
  Info 'Generating the keypair (into a temp dir, deleted on exit)'
  Push-Location $workDir
  try {
    $env:COSIGN_PASSWORD = $s1
    & $Cosign generate-key-pair | Out-Null
  } finally {
    Pop-Location
  }
  if (-not (Test-Path $keyFile) -or -not (Test-Path $pubFile)) { Fail 'cosign did not produce cosign.key / cosign.pub.' }
  Ok "private key: $keyFile (encrypted)"
  Write-Host '  public key:' -ForegroundColor DarkGray
  Get-Content $pubFile | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }

  # ── 4. Install the public key into the repo ────────────────────────────
  Info "Install the public key -> internal/selfupdate/keys/cosign.pub"
  $newPub = Get-Content $pubFile -Raw
  $oldPub = if (Test-Path $pubKeyDest) { Get-Content $pubKeyDest -Raw } else { '' }
  if ($newPub -eq $oldPub) {
    Ok 'already up to date.'
  } elseif (Confirm 'Overwrite the committed cosign.pub with the new key?') {
    Copy-Item $pubFile $pubKeyDest -Force
    Ok 'written.'
  } else {
    Warn 'skipped - the release CI key-diff check will fail until cosign.pub matches.'
  }

  # ── 5. Push the CI secrets ────────────────────────────────────────────
  Info "Push COSIGN_PRIVATE_KEY + COSIGN_PASSWORD to $Repo"
  Get-Content $keyFile -Raw | gh secret set COSIGN_PRIVATE_KEY --repo $Repo
  if ($LASTEXITCODE -ne 0) { Fail 'failed to set COSIGN_PRIVATE_KEY.' }
  Ok 'set COSIGN_PRIVATE_KEY'
  $s1 | gh secret set COSIGN_PASSWORD --repo $Repo
  if ($LASTEXITCODE -ne 0) { Fail 'failed to set COSIGN_PASSWORD.' }
  Ok 'set COSIGN_PASSWORD'

  # ── 6. Back up the private key ────────────────────────────────────────
  Info 'Back up the private key'
  Write-Host '  It now lives only in GitHub secrets and this temp file. Keep an offline'
  Write-Host '  backup so you can rotate or re-add the secret without regenerating.'
  if (Confirm 'Copy cosign.key to a path you choose now?') {
    $dest = Read-Host 'Absolute path to copy cosign.key to'
    if ($dest) {
      New-Item -ItemType Directory -Path (Split-Path $dest) -Force | Out-Null
      Copy-Item $keyFile $dest -Force
      Ok "copied to $dest (keep it secret)."
    }
  } else {
    Warn 'no backup taken - you will have to regenerate the key to rotate it.'
  }

  # ── 7. Commit the public key ─────────────────────────────────────────
  Info 'Commit the public key'
  git -C $repoRoot diff --quiet -- internal/selfupdate/keys/cosign.pub
  if ($LASTEXITCODE -eq 0) {
    Ok 'no change to cosign.pub - nothing to commit.'
  } elseif (Confirm 'Commit internal/selfupdate/keys/cosign.pub now?') {
    git -C $repoRoot add internal/selfupdate/keys/cosign.pub
    git -C $repoRoot commit -m 'Add the real cosign release-signing public key' | Out-Null
    Ok 'committed. Push when ready: git push'
  } else {
    Warn 'commit it yourself: git add internal/selfupdate/keys/cosign.pub; git commit'
  }

  Write-Host ''
  Ok 'Done.'
}
finally {
  $env:COSIGN_PASSWORD = $null
  if (Test-Path $workDir) { Remove-Item $workDir -Recurse -Force -ErrorAction SilentlyContinue }
}
