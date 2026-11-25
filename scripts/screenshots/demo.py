"""The demo workspace behind the pipeline screenshots: five invented pipelines, their scripts, stage titles,
environment, stored secrets and timetables. Nothing here belongs to a real organisation; every value is made up."""

import random
import secrets
from datetime import timedelta

# The runners the demo starts (containers with these hostnames).
RUNNERS = ["runner-1", "runner-2"]

# Stored secrets: name, description. Their values are generated when the demo is set up and never printed.
SECRETS = [
    ("docs-publish-token", "Token that publishes the docs site"),
    ("embedding-api-key", "Key for the embeddings endpoint"),
    ("smtp-password", "Password of the mailbox that sends reports"),
]


def secret_value() -> str:
    return secrets.token_urlsafe(24)


DOCS_SYNC = '''#%% fetch
set -euo pipefail
M="${KERA_MARK:-::}"
echo "${M}steps fetch convert check-links publish"
echo "fetching the docs from branch ${DOCS_BRANCH:-main}"
for part in guides reference changelog; do
  sleep 4
  echo "  fetched $part"
done
echo "${M}count files=148"
#%% convert
echo "converting 148 pages to HTML"
for batch in 1 2 3 4 5; do
  sleep 4
  echo "  batch $batch of 5 done"
done
#%% check links
python3 "$KERA_SCRIPT_DIR/check_links.py" 148
#%% publish
test -n "${PUBLISH_TOKEN:-}" && echo "publish token is set"
echo "uploading the build"
sleep 8
echo "switching the live version"
sleep 4
echo "published 148 pages"
'''

CHECK_LINKS = '''import sys
import time

pages = int(sys.argv[1])
for done in (50, 100, pages):
    time.sleep(3)
    print(f"checked {done} of {pages} pages", flush=True)
print("all links work")
'''

NIGHTLY_REPORT = '''#%% query
import os, time
M = os.environ.get("KERA_MARK", "::")
def say(*a): print(*a, flush=True)
say(M + "steps query render send")
say("reading yesterday's orders")
time.sleep(10)
say("1,284 orders, 37 refunds, 52 new customers")
say(M + "count orders=1284")
#%% render
say("grouping by region")
time.sleep(5)
say(M + "warn 3 orders have no region and are listed under Unassigned")
say("drawing charts")
time.sleep(5)
say("report.pdf is 5 pages")
#%% send
say("emailing the report to " + os.environ.get("REPORT_RECIPIENTS", "nobody"))
time.sleep(5)
say("sent")
'''

TICKET_DIGEST = '''#%% collect
import os, time
M = os.environ.get("KERA_MARK", "::")
def say(*a): print(*a, flush=True)
say(M + "steps collect summarise post")
time.sleep(5)
say("41 tickets opened or updated in the last hour")
say(M + "count tickets=41")
#%% summarise
time.sleep(10)
say("5 topics: login (12), billing (9), exports (8), api (7), other (5)")
#%% post
time.sleep(5)
say("posted the digest to " + os.environ.get("DIGEST_CHANNEL", "the channel"))
'''

INVOICE_CHECK = '''#%% fetch
set -uo pipefail
M="${KERA_MARK:-::}"
echo "${M}steps fetch validate reconcile notify"
echo "looking for new invoices in ${INVOICE_DIR:-inbox}"
sleep 10
echo "41 new invoices"
echo "${M}count invoices=41"
#%% validate
sleep 5
echo "checking totals and tax numbers: ok"
echo "checking purchase order numbers"
sleep 5
echo "INV-2043: no purchase order number"
echo "INV-2051: no purchase order number"
echo "INV-2058: no purchase order number"
echo "${M}fail 3 of 41 invoices have no purchase order number"
exit 1
#%% reconcile
echo "matching invoices with payments"
sleep 15
#%% notify
echo "sending the summary to the finance team"
sleep 5
'''

KB_REFRESH = '''#%% export
set -euo pipefail
M="${KERA_MARK:-::}"
echo "${M}steps export embed swap verify"
echo "exporting published articles"
sleep 6
echo "  2,310 articles written to articles.jsonl"
sleep 8
echo "${M}count articles=2310"
#%% embed
test -n "${EMBED_API_KEY:-}" && echo "embedding key is set"
for batch in 1 2 3 4 5 6 7 8 9 10 11 12; do
  sleep 15
  echo "  batch $batch of 12: $((batch * 200)) articles embedded"
done
#%% swap
echo "building the new index"
sleep 5
echo "switching search to the new index"
sleep 3
#%% verify
echo "running 20 test searches"
sleep 6
echo "20 of 20 searches found the expected article"
'''

# Each pipeline: title, timetable (frequency, minutes after midnight UTC; hourly: minute of the hour), the script files
# (entry named), the environment, and the steps with what an OK run of them looks like. `env` entries are
# (name, value, kind): kind "plain", "secret" (the pipeline's own write-only variable) or the name of a stored secret.
# `steps` entries are (key, title, description, seconds, log lines of an OK run, counts).
PIPELINES = [
    dict(
        name="docs-sync", title="Docs sync", frequency="daily", at=360, entry="run.sh",
        files={"run.sh": DOCS_SYNC, "check_links.py": CHECK_LINKS},
        env=[("DOCS_BRANCH", "main", "plain"), ("PUBLISH_TOKEN", None, "docs-publish-token")],
        steps=[
            ("fetch", "Fetch the docs", "Pull the latest pages from the docs repository.", 12,
             "fetching the docs from branch main\n  fetched guides\n  fetched reference\n  fetched changelog", {"files": 148}),
            ("convert", "Convert to HTML", "Render the Markdown pages with the site template.", 20,
             "converting 148 pages to HTML\n  batch 1 of 5 done\n  batch 2 of 5 done\n  batch 3 of 5 done\n  batch 4 of 5 done\n  batch 5 of 5 done", {}),
            ("check-links", "Check links", "Follow every link and list the broken ones.", 9,
             "checked 50 of 148 pages\nchecked 100 of 148 pages\nchecked 148 of 148 pages\nall links work", {}),
            ("publish", "Publish the site", "Upload the build and switch the live version.", 12,
             "publish token is set\nuploading the build\nswitching the live version\npublished 148 pages", {}),
        ],
        base=55, jitter=0.1, trend=0.0, odd={4: ("slow", 2.4), 9: ("fail", 3)},
        fail_text="Failed at step publish: the upload was refused (HTTP 401).",
    ),
    dict(
        name="nightly-report", title="Nightly report", frequency="daily", at=120, entry="report.py",
        files={"report.py": NIGHTLY_REPORT},
        env=[("REPORT_RECIPIENTS", "finance@example.com", "plain"), ("SMTP_PASSWORD", None, "smtp-password")],
        steps=[
            ("query", "Query yesterday's orders", "Read orders, refunds and new customers from the reporting database.", 10,
             "reading yesterday's orders\n1,284 orders, 37 refunds, 52 new customers", {"orders": 1284}),
            ("render", "Build the report", "Group by region and draw the charts.", 10,
             "grouping by region\ndrawing charts\nreport.pdf is 5 pages", {}),
            ("send", "Email the report", "Send the PDF to the finance list.", 5,
             "emailing the report to finance@example.com\nsent", {}),
        ],
        base=25, jitter=0.12, trend=0.02, odd={},
        warn=("render", "3 orders have no region and are listed under Unassigned"),
    ),
    dict(
        name="ticket-digest", title="Ticket digest", frequency="hourly", at=5, entry="digest.py",
        files={"digest.py": TICKET_DIGEST},
        env=[("DIGEST_CHANNEL", "#support-digest", "plain")],
        steps=[
            ("collect", "Collect new tickets", "Read tickets opened or updated in the last hour.", 5,
             "41 tickets opened or updated in the last hour", {"tickets": 41}),
            ("summarise", "Summarise by topic", "Group tickets by topic and pick the ones waiting longest.", 10,
             "5 topics: login (12), billing (9), exports (8), api (7), other (5)", {}),
            ("post", "Post the digest", "Send the summary to the support channel.", 5,
             "posted the digest to #support-digest", {}),
        ],
        base=20, jitter=0.15, trend=0.0, odd={11: ("slow", 3.0)},
    ),
    dict(
        name="invoice-check", title="Invoice check", frequency="weekdays", at=510, entry="check.sh",
        files={"check.sh": INVOICE_CHECK},
        env=[("INVOICE_DIR", "/work/invoice-check/inbox", "plain"), ("LEDGER_TOKEN", None, "secret")],
        steps=[
            ("fetch", "Fetch new invoices", "Download the PDF invoices that arrived since the last run.", 10,
             "looking for new invoices in /work/invoice-check/inbox\n41 new invoices", {"invoices": 41}),
            ("validate", "Validate invoices", "Check totals, tax numbers and purchase order references.", 10,
             "checking totals and tax numbers: ok\nchecking purchase order numbers\nall 41 invoices are valid", {}),
            ("reconcile", "Match with payments", "Match each invoice with a payment in the ledger.", 15,
             "matching invoices with payments\n41 of 41 matched", {}),
            ("notify", "Notify the finance team", "Send a summary of unmatched invoices.", 5,
             "sending the summary to the finance team", {}),
        ],
        base=40, jitter=0.1, trend=0.0, odd={6: ("fail", 2)},
        fail_text="Failed at step validate: 2 of 38 invoices have no purchase order number.",
    ),
    dict(
        name="kb-refresh", title="Knowledge base refresh", frequency="daily", at=210, entry="refresh.sh",
        files={"refresh.sh": KB_REFRESH},
        env=[("BATCH_SIZE", "200", "plain"), ("EMBED_API_KEY", None, "embedding-api-key")],
        steps=[
            ("export", "Export the articles", "Write every published article to a JSON Lines file.", 14,
             "exporting published articles\n  2,310 articles written to articles.jsonl", {"articles": 2310}),
            ("embed", "Embed in batches", "Create vectors for the articles, 200 at a time.", 180,
             "embedding key is set\n  batch 1 of 12: 200 articles embedded\n  batch 6 of 12: 1200 articles embedded\n  batch 12 of 12: 2310 articles embedded", {}),
            ("swap", "Switch to the new index", "Point search at the new index when it is complete.", 8,
             "building the new index\nswitching search to the new index", {}),
            ("verify", "Verify with test searches", "Run twenty known searches against the new index.", 6,
             "running 20 test searches\n20 of 20 searches found the expected article", {}),
        ],
        base=208, jitter=0.08, trend=0.01, odd={5: ("slow", 1.7), 12: ("fail", 1)},
        fail_text="Failed at step embed: the embeddings endpoint answered HTTP 503.",
    ),
]


def slots(p: dict, now):
    """When the timetable fired in the past two weeks (two days for an hourly one), oldest first."""
    out = []
    if p["frequency"] == "hourly":
        t = now.replace(minute=p["at"], second=0, microsecond=0)
        if t > now:
            t -= timedelta(hours=1)
        out = [t - timedelta(hours=i) for i in range(47, -1, -1)]
    else:
        for d in range(14, -1, -1):
            t = (now - timedelta(days=d)).replace(hour=p["at"] // 60, minute=p["at"] % 60, second=0, microsecond=0)
            if t.weekday() >= 5 and p["frequency"] == "weekdays":
                continue
            out.append(t)
    return [t for t in out if t < now - timedelta(minutes=20)]


def plan(p: dict, now, seed: int):
    """Backdated runs of one pipeline, oldest first, without the newest slot (a real run takes it):
    (start, status, total seconds, queue wait seconds, failing step index, error text)."""
    rng = random.Random(seed)
    sl = slots(p, now)[:-1]
    out = []
    for i, t in enumerate(sl):
        k = len(sl) - i  # runs before the newest one
        total = p["base"] * (1 + p["trend"] * i) * (1 + rng.uniform(-p["jitter"], p["jitter"]))
        status, step, text = "ok", None, ""
        kind = p["odd"].get(k)
        if kind and kind[0] == "slow":
            total *= kind[1]
        elif kind and kind[0] == "fail":
            status, step, text = "failed", kind[1], p["fail_text"]
        elif p.get("warn") and k % 3 == 1:
            status = "warning"
        wait = 38 if k == 3 and p["frequency"] != "hourly" else rng.choice([1, 1, 2, 2, 3, 4])
        out.append((t, status, total, wait, step, text))
    return out
