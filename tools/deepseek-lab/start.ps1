$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
try {
    $labExisting = Invoke-WebRequest -Uri 'http://127.0.0.1:8767/' -UseBasicParsing -TimeoutSec 2
    if ($labExisting.Content -match 'DeepSeek Lab') {
        Start-Process 'http://127.0.0.1:8767/'
        exit 0
    }
} catch { }
python -c "import requests" 2>$null
if ($LASTEXITCODE -ne 0) {
    python -m pip install -r requirements.txt
    if ($LASTEXITCODE -ne 0) { throw 'Dependency installation failed' }
}
python app.py --open
