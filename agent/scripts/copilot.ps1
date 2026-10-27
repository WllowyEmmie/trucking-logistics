# Chat with the Copilot from PowerShell (Windows PowerShell 5.1 or 7).
#   cd agent
#   .\scripts\copilot.ps1 -Email you@example.com
# Optional: -Api http://localhost:8080 -Agent http://localhost:8200
# Type a request; "new" starts a new conversation, "quit" stops.
param(
    [Parameter(Mandatory = $true)][string]$Email,
    [string]$Api = "http://localhost:8080",
    [string]$Agent = "http://localhost:8200"
)

Add-Type -AssemblyName System.Net.Http

# ---- sign in (the password is asked for, never stored) ----
$secure = Read-Host "Password for $Email" -AsSecureString
$plain = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
try {
    $login = Invoke-WebRequest "$Api/auth/login" -Method Post -UseBasicParsing -ContentType "application/json" `
        -Body (@{ email = $Email; password = $plain } | ConvertTo-Json)
} catch {
    Write-Host "Sign-in failed: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
$plain = $null
# The session comes back as a cookie. Read it from the Set-Cookie header
# itself: the cookie is marked Secure (COOKIE_SECURE), so PowerShell's cookie
# jar won't hand it back for a plain http://localhost address.
$setCookie = ($login.Headers["Set-Cookie"] | ForEach-Object { $_ }) -join "; "
$token = ([regex]'session=([^;,\s]+)').Match($setCookie).Groups[1].Value
if (-not $token) { Write-Host "Signed in, but no session came back." -ForegroundColor Red; exit 1 }

$http = New-Object System.Net.Http.HttpClient
$http.Timeout = [TimeSpan]::FromMinutes(5)
$http.DefaultRequestHeaders.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $token)

# ---- Markdown, rendered for the terminal ----
# The model answers in Markdown. Each line is rendered as soon as it's
# complete: headings and **bold** in bold, `code` in cyan, *italics*, bullets
# as dots, and tables as aligned rows. Works in Windows PowerShell 5.1 and 7.
$E = [char]27
# Non-ASCII built from code points: Windows PowerShell 5.1 reads a script
# without a byte-order mark as ANSI, which would garble them.
$Dot = [char]0x2022; $Rule = [string][char]0x2500; $Bar = [char]0x2502
function Format-Inline([string]$t) {
    $t = [regex]::Replace($t, '\*\*(.+?)\*\*', "$E[1m`$1$E[22m")
    $t = [regex]::Replace($t, '__(.+?)__', "$E[1m`$1$E[22m")
    $t = [regex]::Replace($t, '`([^`]+)`', "$E[36m`$1$E[39m")
    $t = [regex]::Replace($t, '(?<![\w*])\*(?!\s)(.+?)(?<!\s)\*(?![\w*])', "$E[3m`$1$E[23m")
    $t = [regex]::Replace($t, '\[([^\]]+)\]\(([^)]+)\)', "`$1 $E[2m(`$2)$E[22m")
    return $t
}
$script:table = @()
function Write-TableRows {
    if ($script:table.Count -eq 0) { return }
    # A list of cell arrays (a plain pipeline would flatten a one-row table).
    $rows = New-Object System.Collections.ArrayList
    foreach ($t in $script:table) { [void]$rows.Add([string[]]@($t.Trim().Trim('|').Split('|') | ForEach-Object { $_.Trim() })) }
    $cols = 0
    foreach ($r in $rows) { if ($r.Count -gt $cols) { $cols = $r.Count } }
    $width = @(0) * $cols
    foreach ($r in $rows) { for ($i = 0; $i -lt $r.Count; $i++) { $plain = $r[$i] -replace '\*\*|`', ''; if ($plain.Length -gt $width[$i]) { $width[$i] = $plain.Length } } }
    $first = $true
    foreach ($r in $rows) {
        $cells = for ($i = 0; $i -lt $cols; $i++) {
            $cell = if ($i -lt $r.Count) { $r[$i] } else { "" }
            $pad = $width[$i] - ($cell -replace '\*\*|`', '').Length
            (Format-Inline $cell) + (" " * [Math]::Max(0, $pad))
        }
        $line = "  " + ($cells -join "  ")
        if ($first) { Write-Host "$E[1m$line$E[22m"; $first = $false } else { Write-Host $line }
    }
    $script:table = @()
}
function Write-MarkdownLine([string]$line) {
    if ($line -match '^\s*\|.*\|\s*$') {
        if ($line -notmatch '^\s*\|[\s:|-]+\|\s*$') { $script:table += $line }   # skip |---|---|
        return
    }
    Write-TableRows
    if ($line -match '^(#{1,6})\s+(.*)$') { Write-Host "$E[1;4m$(Format-Inline $Matches[2])$E[22;24m"; return }
    if ($line -match '^(\s*)[-*+]\s+(.*)$') { Write-Host "$($Matches[1])  $Dot $(Format-Inline $Matches[2])"; return }
    if ($line -match '^(\s*)(\d+)[.)]\s+(.*)$') { Write-Host "$($Matches[1])  $($Matches[2]). $(Format-Inline $Matches[3])"; return }
    if ($line -match '^\s*([-*_]\s*){3,}$') { Write-Host "  $E[2m$($Rule * 40)$E[22m"; return }
    if ($line -match '^>\s?(.*)$') { Write-Host "  $E[2m$Bar$E[22m $(Format-Inline $Matches[1])"; return }
    Write-Host (Format-Inline $line)
}

# Sends one request and prints the stream. Returns @{ thread = ...; pending = ... }.
function Send-Copilot([string]$path, $body) {
    $json = $body | ConvertTo-Json -Depth 8 -Compress
    $req = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::Post, "$Agent$path")
    $req.Content = New-Object System.Net.Http.StringContent($json, [Text.Encoding]::UTF8, "application/json")
    try {
        $res = $http.SendAsync($req, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
    } catch {
        # No answer at all: usually the agent isn't running on $Agent.
        $msg = $_.Exception.Message
        if ($_.Exception.InnerException) { $msg = $_.Exception.InnerException.Message }
        Write-Host "! Can't reach the agent at $Agent ($msg). Is uvicorn running?" -ForegroundColor Red
        return @{ thread = $null; pending = $null }
    }
    if (-not $res.IsSuccessStatusCode) {
        Write-Host "! $([int]$res.StatusCode): $($res.Content.ReadAsStringAsync().Result)" -ForegroundColor Red
        return @{ thread = $null; pending = $null }
    }
    $reader = New-Object IO.StreamReader($res.Content.ReadAsStreamAsync().Result)
    $out = @{ thread = $null; pending = $null }
    $event = ""; $streamed = $false; $buf = ""
    while ($null -ne ($line = $reader.ReadLine())) {
        if ($line.StartsWith("event: ")) { $event = $line.Substring(7); continue }
        if (-not $line.StartsWith("data: ")) { continue }
        $data = $line.Substring(6) | ConvertFrom-Json
        switch ($event) {
            "thread"     { $out.thread = $data.thread_id }
            "token"      {
                # Render whole lines as they complete; keep the rest for later.
                $buf += $data.text; $streamed = $true
                while (($nl = $buf.IndexOf("`n")) -ge 0) { Write-MarkdownLine $buf.Substring(0, $nl).TrimEnd("`r"); $buf = $buf.Substring($nl + 1) }
            }
            "message"    {
                $rest = if ($streamed) { $buf } else { $data.text }
                foreach ($l in ($rest -split "`r?`n")) { Write-MarkdownLine $l }
                Write-TableRows
                Write-Host ""; $buf = ""; $streamed = $false
            }
            "tool_start" { Write-Host "  -> $($data.name) $($data.args | ConvertTo-Json -Compress -Depth 6)  [$($data.tier)]" -ForegroundColor DarkGray }
            "tool_end"   { $mark = if ($data.ok) { "ok" } else { "refused" }; Write-Host "  <- $($data.name): $mark" -ForegroundColor DarkGray }
            "ui"         { Write-Host "  [console] $($data | ConvertTo-Json -Compress)" -ForegroundColor DarkGray }
            "file"       {
                # A file the Copilot made (a PDF report): save it in this folder.
                $name = ($data.name -replace '[^A-Za-z0-9._-]', '_')
                $dest = Join-Path (Get-Location) $name
                $bytes = $http.GetByteArrayAsync("$Agent$($data.url)").Result
                [IO.File]::WriteAllBytes($dest, $bytes)
                Write-Host "  [file] saved $dest ($([int]($bytes.Length / 1024)) KB)" -ForegroundColor DarkGray
            }
            "interrupt"  { $out.pending = $data }
            "error"      { Write-Host "! $($data.message)" -ForegroundColor Red }
        }
    }
    $reader.Dispose()
    return $out
}

# Asks approve / reject (/ edit) for each waiting change, in order.
function Read-Decisions($pending) {
    Write-Host "`nWaiting for your approval:" -ForegroundColor Yellow
    $decisions = @()
    $i = 1
    foreach ($a in $pending.actions) {
        Write-Host ("  {0}. {1} [{2}] {3}" -f $i, $a.name, $a.tier, ($a.args | ConvertTo-Json -Compress -Depth 6)) -ForegroundColor Yellow
        $allowed = @($a.allowed_decisions)
        while ($true) {
            $c = (Read-Host ("     " + ($allowed -join "/"))).Trim().ToLower()
            if (($c -eq "a" -or $c -eq "approve") -and $allowed -contains "approve") { $decisions += @{ type = "approve" }; break }
            if (($c -eq "r" -or $c -eq "reject") -and $allowed -contains "reject") {
                $note = Read-Host "     note for the Copilot (optional)"
                if (-not $note) { $note = "Rejected." }
                $decisions += @{ type = "reject"; message = $note }; break
            }
            if (($c -eq "e" -or $c -eq "edit") -and $allowed -contains "edit") {
                $raw = Read-Host "     new args as JSON (Enter keeps them)"
                try {
                    $newArgs = if ($raw) { $raw | ConvertFrom-Json } else { $a.args }
                    $decisions += @{ type = "edit"; args = $newArgs }; break
                } catch { Write-Host "     not valid JSON" }
                continue
            }
            Write-Host ("     type one of: " + ($allowed -join ", "))
        }
        $i++
    }
    return ,$decisions
}

Write-Host "Signed in. Talk to the Copilot; 'new' starts over, 'quit' stops.`n" -ForegroundColor DarkGray
$thread = $null
while ($true) {
    $text = Read-Host "you"
    if (-not $text) { continue }
    if ($text -eq "quit") { break }
    if ($text -eq "new") { $thread = $null; Write-Host "(new conversation)" -ForegroundColor DarkGray; continue }

    $body = @{ message = $text }
    if ($thread) { $body.thread_id = $thread }
    $r = Send-Copilot "/copilot/chat" $body
    if ($r.thread) { $thread = $r.thread }
    while ($r.pending -and $thread) {
        $d = Read-Decisions $r.pending
        $r = Send-Copilot "/copilot/resume" @{ thread_id = $thread; decisions = $d }
    }
}
$http.Dispose()
