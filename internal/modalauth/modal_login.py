"""Modal Google OAuth login helper.

Drives a Camoufox (Playwright) browser to:
  1. open the Modal login page (https://modal.com/login),
  2. click "Continue with Google",
  3. fill the Google email / password (and optional recovery-email challenge),
  4. accept the OAuth consent screen,
  5. wait for the redirect back to Modal and capture the session cookies.

The captured cookies (a "name=value; ..." Cookie header string) are written as
JSON to the path given by --out so the calling Go panel can import the account.
"""

import argparse
import json
import os
import shutil
import sys
import tempfile
import time
from typing import List, Optional
from urllib.parse import urlparse


PROJECT_DIR = os.path.dirname(os.path.abspath(__file__))
VENDOR_DIR = os.path.join(PROJECT_DIR, ".vendor")
if os.path.isdir(VENDOR_DIR) and VENDOR_DIR not in sys.path:
    sys.path.insert(0, VENDOR_DIR)

from camoufox.sync_api import Camoufox


DEFAULT_BASE_URL = "https://modal.com"
DEFAULT_TIMEOUT = 300  # seconds for the whole OAuth flow (allows 2FA)

# ---- Modal login page ----
MODAL_GOOGLE_SELECTORS = [
    "a:has-text('Continue with Google')",
    "button:has-text('Continue with Google')",
    "a:has-text('Google')",
    "button:has-text('Google')",
    "a[href*='google']",
    "a[href*='oauth']",
    "[data-provider='google']",
    "[aria-label*='Google' i]",
    "a:has(img[alt*='Google' i])",
    "button:has(img[alt*='Google' i])",
]

# ---- Google login form selectors ----
EMAIL_SELECTORS = [
    'input[type="email"]',
    "#identifierId",
    'input[name="identifier"]',
    'input[autocomplete="username"]',
]
EMAIL_NEXT_SELECTORS = [
    "#identifierNext",
    "#identifierNext button",
    "div#identifierNext",
]
PWD_SELECTORS = [
    'input[type="password"]',
    'input[name="Passwd"]',
    'input[name="password"]',
]
PWD_NEXT_SELECTORS = [
    "#passwordNext",
    "#passwordNext button",
    "div#passwordNext",
]
NEXT_TEXT_SELECTORS = [
    "button:has-text('Next')",
    "button:has-text('下一步')",
    "button:has-text('Tiếp theo')",
    "[role='button']:has-text('Next')",
]
CHOOSER_TEXTS = [
    "Use another account",
    "another account",
    "使用另一个账号",
    "使用其他账号",
    "其他账号",
    "Dùng một tài khoản khác",
    "Sử dụng tài khoản khác",
    "tài khoản khác",
]

CONSENT_TEXTS = [
    "Continue",
    "继续",
    "Allow",
    "允许",
    "I agree",
    "同意",
    "Agree",
    "同意继续",
    "Accept",
    "I accept",
    "接受",
    "我接受",
    "Acknowledge",
    "Yes",
    "我同意",
    "Tiếp tục",
    "Cho phép",
    "Đồng ý",
]
CONSENT_SCREEN_TEXTS = [
    "wants to access your Google Account",
    "wants to access",
    "access your Google Account",
    "希望访问您的 Google 帐号",
    "想访问您的 Google 账号",
    "访问您的 Google 账号",
    "访问您的 Google 帐号",
    "muốn truy cập Tài khoản Google",
]
SPEEDBUMP_TEXTS = [
    "I accept",
    "I accept the above",
    "I accept the",
    "Accept",
    "接受",
    "I agree",
    "Agree",
    "Agree to terms",
    "I understand",
    "Got it",
    "我了解",
    "已了解",
    "我同意",
    "同意",
    "Continue",
    "继续",
    "Next",
    "下一步",
    "Confirm",
    "确认",
]

RECOVERY_EMAIL_TEXTS = [
    "确认您的辅助邮箱",
    "确认辅助邮箱",
    "辅助邮箱",
    "Confirm your recovery email",
    "Confirm your recovery email address",
    "Confirm a recovery email",
    "recovery email",
    "Xác nhận email khôi phục",
    "email khôi phục",
]
RECOVERY_EMAIL_INPUT_SELECTORS = [
    'input[type="email"]',
    'input[name="email"]',
    'input[name="recoveryEmail"]',
    'input[autocomplete="email"]',
    'input[name="identifier"]',
    'input[type="text"]',
]
RECOVERY_NEXT_SELECTORS = [
    "#next",
    "#next button",
    "button:has-text('Next')",
    "button:has-text('下一步')",
    "button:has-text('继续')",
    "button:has-text('Tiếp theo')",
    "[role='button']:has-text('Next')",
    "button[type='submit']",
]


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Modal Google OAuth login/signup via Camoufox.")
    p.add_argument("--email", required=True)
    p.add_argument("--password", required=True)
    p.add_argument("--aux-email", default="", help="recovery email for Google's challenge")
    p.add_argument("--base-url", default=DEFAULT_BASE_URL)
    p.add_argument("--proxy", default=os.environ.get("MODAL_PROXY", ""))
    p.add_argument("--headless", action="store_true")
    p.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT)
    p.add_argument("--mode", choices=["login", "signup"], default="signup",
                   help="login = existing account, signup = register new account (default)")
    p.add_argument("--out", required=True, help="path to write the JSON result file")
    return p.parse_args()


def _playwright_proxy(proxy_url: str) -> Optional[dict]:
    if not proxy_url:
        return None
    p = urlparse(proxy_url)
    if not p.hostname:
        return None
    server = f"{p.scheme or 'http'}://{p.hostname}" + (f":{p.port}" if p.port else "")
    arg = {"server": server}
    if p.username:
        arg["username"] = p.username
    if p.password:
        arg["password"] = p.password
    return arg


def _host_name(page) -> str:
    try:
        return page.evaluate("location.hostname") or ""
    except Exception:
        return ""


def _first_visible(page, selectors, timeout: int = 15):
    deadline = time.time() + timeout
    while time.time() < deadline:
        for sel in selectors:
            loc = page.locator(sel).first
            try:
                if loc.count() > 0 and loc.is_visible():
                    return loc
            except Exception:
                continue
        time.sleep(0.3)
    return None


def _click_text_button(page, texts: List[str], label: str = "click", timeout: int = 20) -> bool:
    css_templates = [
        "button:has-text('{}')",
        "a:has-text('{}')",
        "[role='button']:has-text('{}')",
        "input[type='button'][value*='{}']",
        "input[type='submit'][value*='{}']",
    ]
    deadline = time.time() + timeout
    while time.time() < deadline:
        for txt in texts:
            for template in css_templates:
                try:
                    sel = template.format(txt.replace("'", "\\'"))
                    loc = page.locator(sel).first
                    if loc.count() > 0 and loc.is_visible():
                        print(f"[{label}] clicking '{txt}' via {sel}")
                        loc.click()
                        return True
                except Exception:
                    continue
            try:
                cand = page.get_by_role("button", name=txt, exact=False).first
                if cand.count() > 0 and cand.is_visible():
                    print(f"[{label}] clicking '{txt}' via get_by_role(button)")
                    cand.click()
                    return True
            except Exception:
                pass
            try:
                cand = page.get_by_text(txt, exact=False).first
                if cand.count() > 0 and cand.is_visible():
                    tag = cand.evaluate("e => e.tagName.toLowerCase()")
                    role = cand.get_attribute("role") or ""
                    if tag in ("button", "a", "input") or role == "button":
                        print(f"[{label}] clicking '{txt}' via get_by_text (<{tag}>)")
                        cand.click()
                        return True
            except Exception:
                pass
            try:
                clicked = page.evaluate(
                    """(text) => {
                        const all = document.querySelectorAll('button, a, [role="button"], div, span, input[type="button"], input[type="submit"]');
                        for (const el of all) {
                            const t = (el.innerText || el.value || '').trim();
                            if (t.includes(text) && el.offsetParent !== null) {
                                el.scrollIntoView({block: 'center', inline: 'center'});
                                el.click();
                                return true;
                            }
                        }
                        return false;
                    }""",
                    txt,
                )
                if clicked:
                    print(f"[{label}] clicked '{txt}' via JavaScript")
                    return True
            except Exception:
                pass
        time.sleep(1)
    return False


def _has_visible_text(page, texts) -> bool:
    for txt in texts:
        try:
            loc = page.get_by_text(txt, exact=False).first
            if loc.count() > 0 and loc.is_visible():
                return True
        except Exception:
            continue
    return False


def _is_modal_page(page, base_host: str) -> bool:
    host = _host_name(page)
    return bool(host) and (host == base_host or host.endswith("." + base_host))


def _is_modal_logged_in(page, base_host: str) -> bool:
    host = _host_name(page)
    if host != base_host:
        return False
    try:
        path = (urlparse(page.url).path or "").rstrip("/")
    except Exception:
        return False
    # Modal redirects to dashboard/workspaces after login
    if path in ("/login", "/login/sso", "/signup"):
        return False
    return True


def _is_google_page(page) -> bool:
    host = _host_name(page)
    return host == "accounts.google.com" or host.endswith(".google.com")


def _google_block_reason(page) -> str:
    url = page.url or ""
    if "/signin/rejected" in url or "signin/rejected" in url:
        return "Google rejected the sign-in (account may be blocked, disabled, or flagged for unusual activity)"
    try:
        lower_text = (page.inner_text("body") or "").lower()
    except Exception:
        lower_text = ""
    markers = [
        ("couldn't verify", "Google could not verify this account belongs to you"),
        ("couldn't sign you in", "Google could not sign you in"),
        ("unusual activity", "Google flagged unusual activity and blocked the sign-in"),
        ("suspended", "this Google account appears to be suspended"),
        ("disabled", "this Google account appears to be disabled"),
        ("verify it's you", "Google requires additional verification (please log in manually)"),
        ("2-step", "Google is asking for 2-step verification (please log in manually)"),
    ]
    for needle, reason in markers:
        if needle in lower_text:
            return reason
    return ""


def _is_google_email_page(page) -> bool:
    url = page.url
    if "/identifier" in url or "/signin/challenge/identifier" in url:
        return True
    return bool(_first_visible(page, EMAIL_SELECTORS, timeout=3))


def _is_google_password_page(page) -> bool:
    url = page.url
    if "/challenge/pwd" in url or "/signin/challenge/pwd" in url:
        return True
    return bool(_first_visible(page, PWD_SELECTORS, timeout=3))


def _handle_modal_login_page(page) -> bool:
    btn = _first_visible(page, MODAL_GOOGLE_SELECTORS, timeout=3)
    if btn:
        print("[modal] clicking 'Continue with Google'...")
        try:
            btn.click(timeout=10_000)
        except Exception:
            btn.click()
        return True
    return False


def _handle_google_email_page(page, email: str) -> bool:
    email_el = _first_visible(page, EMAIL_SELECTORS, timeout=3)
    if not email_el:
        for txt in CHOOSER_TEXTS:
            try:
                cand = page.get_by_text(txt, exact=False).first
                if cand.count() > 0 and cand.is_visible():
                    print(f"[login] account chooser -> clicking '{txt}'")
                    cand.click()
                    time.sleep(2)
                    email_el = _first_visible(page, EMAIL_SELECTORS, timeout=3)
                    break
            except Exception:
                continue
    if email_el:
        email_el.fill(email)
        print(f"[login] filled email: {email}")
        btn = _first_visible(page, EMAIL_NEXT_SELECTORS, timeout=3) or _first_visible(page, NEXT_TEXT_SELECTORS, timeout=3)
        if btn:
            btn.click()
            print("[login] submitted email")
            return True
    return False


def _handle_google_password_page(page, password: str) -> bool:
    pwd_el = _first_visible(page, PWD_SELECTORS, timeout=3)
    if pwd_el:
        pwd_el.fill(password)
        print("[login] filled password")
        btn = _first_visible(page, PWD_NEXT_SELECTORS, timeout=3) or _first_visible(page, NEXT_TEXT_SELECTORS, timeout=3)
        if btn:
            btn.click()
            print("[login] submitted password")
            return True
    return False


def _maybe_confirm_recovery_email(page, aux_email: str, timeout: int = 12) -> bool:
    if not aux_email:
        return False
    challenge_loc = None
    deadline = time.time() + timeout
    while time.time() < deadline:
        for txt in RECOVERY_EMAIL_TEXTS:
            try:
                cand = page.get_by_text(txt, exact=False).first
                if cand.count() > 0 and cand.is_visible():
                    challenge_loc = cand
                    print(f"[recovery] detected challenge text: '{txt}'")
                    break
            except Exception:
                continue
        if challenge_loc:
            break
        host = _host_name(page)
        if host and "google.com" not in host:
            return False
        if _has_visible_text(page, CONSENT_SCREEN_TEXTS):
            return False
        time.sleep(0.5)
    if not challenge_loc:
        return False
    clicked = False
    for target in [challenge_loc]:
        for force in (True, False):
            try:
                target.click(force=force, timeout=8000)
                print(f"[recovery] clicked challenge (force={force})")
                clicked = True
                break
            except Exception:
                continue
        if clicked:
            break
    time.sleep(1.5)
    email_el = _first_visible(page, RECOVERY_EMAIL_INPUT_SELECTORS, timeout=15)
    if not email_el:
        print("[recovery] no input appeared after challenge")
        return False
    try:
        email_el.fill("")
    except Exception:
        pass
    email_el.fill(aux_email)
    print(f"[recovery] filled recovery email: {aux_email}")
    btn = _first_visible(page, RECOVERY_NEXT_SELECTORS, timeout=10)
    if btn:
        btn.click()
        print("[recovery] submitted recovery email")
    time.sleep(2)
    return True


def _check_terms_checkboxes(page, label: str = "login") -> bool:
    action = False
    try:
        for cb in page.locator('input[type="checkbox"]').all():
            try:
                if cb.is_visible() and not cb.is_checked():
                    print(f"[{label}] checking terms/privacy checkbox")
                    cb.check()
                    action = True
                    time.sleep(0.5)
            except Exception:
                continue
    except Exception:
        pass
    try:
        for cb in page.locator('[role="checkbox"]').all():
            try:
                if cb.is_visible():
                    checked = (cb.get_attribute("aria-checked") or "").lower() == "true"
                    if not checked:
                        print(f"[{label}] checking role=checkbox")
                        cb.click()
                        action = True
                        time.sleep(0.5)
            except Exception:
                continue
    except Exception:
        pass
    return action


def _is_speedbump_page(page) -> bool:
    url = page.url or ""
    if "speedbump" in url or "workspacetermsofservice" in url or "termsofservice" in url:
        return True
    return False


def _click_accept_button(page, texts, label: str = "terms", timeout: int = 6) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        for txt in texts:
            for sel in (f"button:has-text('{txt}')", f"[role='button']:has-text('{txt}')"):
                try:
                    loc = page.locator(sel).first
                    if loc.count() > 0 and loc.is_visible():
                        disabled = False
                        try:
                            disabled = loc.is_disabled()
                        except Exception:
                            disabled = False
                        if not disabled:
                            print(f"[{label}] clicking accept button '{txt}' via {sel}")
                            loc.click()
                            return True
                except Exception:
                    continue
        try:
            for el in page.locator("button, [role='button']").all():
                try:
                    if not el.is_visible():
                        continue
                    t = (el.inner_text() or "").strip()
                    if not any(x in t for x in texts):
                        continue
                    href = ""
                    tag = el.evaluate("e => e.tagName.toLowerCase()")
                    if tag == "a":
                        href = el.get_attribute("href") or ""
                        if "policies.google.com" in href or "/terms" in href or "/privacy" in href:
                            continue
                    print(f"[{label}] clicking accept '{t[:40]}' (<{tag}>)")
                    el.click()
                    return True
                except Exception:
                    continue
        except Exception:
            pass
        time.sleep(0.8)
    return False


def _handle_google_speedbump_page(page) -> bool:
    action = _check_terms_checkboxes(page, label="terms")
    if _click_accept_button(page, SPEEDBUMP_TEXTS, label="terms", timeout=6):
        action = True
    return action


def _handle_google_consent_page(page) -> bool:
    action = _check_terms_checkboxes(page, label="consent")
    if _click_text_button(page, CONSENT_TEXTS, label="consent", timeout=4):
        action = True
    return action


def _capture_modal_cookies(context, base_host: str) -> str:
    pairs = []
    names = []
    all_related = []
    for c in context.cookies():
        domain = c.get("domain", "")
        if base_host in domain:
            all_related.append(f"{c['name']}@{domain}")
        if domain == base_host or domain == "." + base_host:
            pairs.append(f"{c['name']}={c['value']}")
            names.append(c["name"])
    print(f"[auth] all modal-related cookies: {', '.join(all_related)}")
    cookie_str = "; ".join(pairs)
    print(f"[auth] captured {len(pairs)} app-domain cookies: {', '.join(names)}")
    return cookie_str


def _write_result(out_path: str, ok: bool, **fields) -> None:
    payload = {"ok": ok}
    payload.update(fields)
    try:
        with open(out_path, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False)
    except Exception as e:
        print(f"[result] failed to write {out_path}: {e}")


def modal_google_login(context, page, email: str, password: str, aux_email: str, base_url: str, mode: str, timeout: int) -> dict:
    base_host = (urlparse(base_url).hostname or "").lower()
    if not base_host:
        base_host = urlparse(DEFAULT_BASE_URL).hostname

    if mode == "signup":
        start_url = base_url.rstrip("/") + "/signup"
    else:
        start_url = base_url.rstrip("/") + "/login"
    print(f"[modal] [{email}] visiting {start_url} (mode={mode})")
    for attempt in range(1, 4):
        try:
            page.goto(start_url, wait_until="domcontentloaded", timeout=60_000)
            break
        except Exception as e:
            print(f"[modal] [{email}] goto failed (attempt {attempt}/3): {e}")
            if attempt == 3:
                raise
            time.sleep(2)
    time.sleep(3)

    state = {
        "google_clicked": False,
        "email_filled": False,
        "pwd_filled": False,
        "recovery_done": False,
    }
    went_to_google = False
    last_urls: dict = {}
    url_settle_until: dict = {}
    url_actions: dict = {}
    app_page_since: Optional[float] = None
    monitor_start = time.time()

    def _url_path(url: str) -> str:
        try:
            parsed = urlparse(url)
            return f"{parsed.hostname}{parsed.path}"
        except Exception:
            return url

    def _mark_action(url: str, action: str) -> None:
        url_actions.setdefault(_url_path(url), set()).add(action)

    def _action_taken(url: str, action: str) -> bool:
        return action in url_actions.get(_url_path(url), set())

    print("[monitor] starting real-time page monitoring...")
    deadline = time.time() + timeout
    while time.time() < deadline:
        pages = list(context.pages)
        any_google_page = False
        any_modal_app = False

        for p in pages:
            try:
                url = p.url
                pid = id(p)
                if last_urls.get(pid) != url:
                    last_urls[pid] = url
                    url_settle_until[pid] = time.time() + 1.5
                    print(f"[monitor] [{pid}] {_host_name(p)} -> {url}")
                    continue
                if time.time() < url_settle_until.get(pid, 0):
                    continue

                if _is_google_page(p):
                    any_google_page = True
                    went_to_google = True
                    block_reason = _google_block_reason(p)
                    if block_reason:
                        raise RuntimeError("google sign-in blocked: " + block_reason)
                    if _is_google_email_page(p):
                        if not state["email_filled"]:
                            if _handle_google_email_page(p, email):
                                state["email_filled"] = True
                    elif _is_google_password_page(p):
                        if not state["pwd_filled"]:
                            if _handle_google_password_page(p, password):
                                state["pwd_filled"] = True
                    else:
                        if aux_email and not state["recovery_done"]:
                            if _maybe_confirm_recovery_email(p, aux_email, timeout=10):
                                state["recovery_done"] = True
                                continue
                        if _is_speedbump_page(p) and not _action_taken(url, "terms"):
                            if _handle_google_speedbump_page(p):
                                _mark_action(url, "terms")
                                continue
                        if not _action_taken(url, "consent"):
                            if _handle_google_consent_page(p):
                                _mark_action(url, "consent")

                elif _is_modal_page(p, base_host):
                    if not state["google_clicked"]:
                        if _handle_modal_login_page(p):
                            state["google_clicked"] = True
                    if _is_modal_logged_in(p, base_host):
                        any_modal_app = True
            except Exception as e:
                print(f"[monitor] error on page {id(p)}: {e}")

        if any_modal_app and not any_google_page and (went_to_google or state["google_clicked"]):
            if app_page_since is None:
                app_page_since = time.time()
                print("[modal] app page detected, waiting for session to settle...")
            elif time.time() - app_page_since > 8:
                print("[modal] login completed, capturing cookies...")
                cookies = _capture_modal_cookies(context, base_host)
                return {
                    "cookie": cookies,
                    "email": email,
                    "workspaceUrl": next((p.url for p in pages if _is_modal_logged_in(p, base_host)), ""),
                }
        else:
            app_page_since = None

        time.sleep(1)

    raise RuntimeError("timed out waiting for Modal Google login to complete")


def run(args: argparse.Namespace) -> int:
    base_url = args.base_url.rstrip("/") or DEFAULT_BASE_URL
    user_data_dir = tempfile.mkdtemp(prefix="modal_camoufox_")
    launch_kwargs = dict(
        headless=args.headless,
        os=os.environ.get("CAMOUFOX_OS", "windows"),
        locale=os.environ.get("CAMOUFOX_LOCALE", "zh-CN"),
        humanize=False,
        block_webrtc=True,
        persistent_context=True,
        user_data_dir=user_data_dir,
        ignore_https_errors=True,
        viewport={"width": 1280, "height": 720},
        firefox_user_prefs={
            "permissions.default.image": 2,
            "permissions.default.media": 2,
            "media.autoplay.default": 5,
            "browser.cache.disk.enable": False,
            "browser.sessionstore.resume_from_crash": False,
            "browser.tabs.firefox-view": False,
            "browser.toolbars.bookmarks.visibility": "never",
            "datareporting.healthreport.uploadEnabled": False,
            "toolkit.telemetry.enabled": False,
            "media.gmp-provider.enabled": False,
            "security.sandbox.content.level": 0,
            "security.sandbox.gpu.level": 0,
        },
    )
    # Disable sandbox on Linux (needed for containers/root)
    import platform as _platform
    if _platform.system() == "Linux":
        launch_kwargs["sandbox"] = False
        launch_kwargs["firefox_user_prefs"]["media.cubeb.sandbox"] = False
    pw_proxy = _playwright_proxy(args.proxy)
    if pw_proxy:
        launch_kwargs["proxy"] = pw_proxy
    print(f"[browser] launching Camoufox for {args.email} (headless={args.headless})")
    try:
        with Camoufox(**launch_kwargs) as context:
            page = context.pages[0] if context.pages else context.new_page()
            page.add_init_script(
                """
                (() => {
                    const swallow = (e) => { e.preventDefault(); e.stopPropagation(); };
                    window.addEventListener('error', swallow, true);
                    window.addEventListener('unhandledrejection', swallow, true);
                    window.onerror = function() { return true; };
                })();
                """
            )
            page.on("pageerror", lambda err: print(f"[browser] pageerror swallowed: {err}"))
            result = modal_google_login(context, page, args.email, args.password, args.aux_email, base_url, args.mode, args.timeout)
        _write_result(args.out, True, **result)
        print(f"[result] ok cookie_len={len(result.get('cookie', ''))} workspace={result.get('workspaceUrl', '')}")
        return 0
    except Exception as e:
        print(f"[error] {e}")
        _write_result(args.out, False, error=str(e))
        return 1
    finally:
        shutil.rmtree(user_data_dir, ignore_errors=True)


def main() -> None:
    args = parse_args()
    try:
        code = run(args)
    except Exception as e:
        print(f"[error] {e}")
        _write_result(args.out, False, error=str(e))
        code = 1
    sys.exit(code)


if __name__ == "__main__":
    main()
