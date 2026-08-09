"""Modal post-login automation.

After a Google OAuth login/signup, this script:
  1. Creates a shared endpoint with the Kimi K3 model and captures the base URL + API key.
  2. Extracts the Stripe "add payment method" checkout link.

The script takes a captured Modal session cookie (from modal_login.py) and drives
the Modal dashboard via a Camoufox browser.
"""

import argparse
import json
import os
import shutil
import sys
import tempfile
import time
from typing import Optional
from urllib.parse import urlparse


PROJECT_DIR = os.path.dirname(os.path.abspath(__file__))
VENDOR_DIR = os.path.join(PROJECT_DIR, ".vendor")
if os.path.isdir(VENDOR_DIR) and VENDOR_DIR not in sys.path:
    sys.path.insert(0, VENDOR_DIR)

from camoufox.sync_api import Camoufox


DEFAULT_BASE_URL = "https://modal.com"
DEFAULT_TIMEOUT = 180


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Modal post-login automation (endpoint + stripe).")
    p.add_argument("--cookie", required=True, help="Modal session cookie string")
    p.add_argument("--base-url", default=DEFAULT_BASE_URL)
    p.add_argument("--proxy", default=os.environ.get("MODAL_PROXY", ""))
    p.add_argument("--headless", action="store_true")
    p.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT)
    p.add_argument("--model", default="kimi-k3", help="model to select (default: kimi-k3)")
    p.add_argument("--out", required=True, help="path to write JSON result")
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


def _set_cookies(context, cookie_str: str, base_host: str):
    cookies = []
    for pair in cookie_str.split(";"):
        pair = pair.strip()
        if "=" not in pair:
            continue
        name, value = pair.split("=", 1)
        cookies.append({
            "name": name.strip(),
            "value": value.strip(),
            "domain": "." + base_host,
            "path": "/",
        })
    if cookies:
        context.add_cookies(cookies)


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


def _click_text_button(page, texts, label: str = "click", timeout: int = 15) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        for txt in texts:
            for template in ["button:has-text('{}')", "a:has-text('{}')", "[role='button']:has-text('{}')"]:
                try:
                    sel = template.format(txt.replace("'", "\\'"))
                    loc = page.locator(sel).first
                    if loc.count() > 0 and loc.is_visible():
                        print(f"[{label}] clicking '{txt}'")
                        loc.click(timeout=5000)
                        return True
                except Exception:
                    continue
            try:
                cand = page.get_by_text(txt, exact=False).first
                if cand.count() > 0 and cand.is_visible():
                    tag = cand.evaluate("e => e.tagName.toLowerCase()")
                    if tag in ("button", "a") or cand.get_attribute("role") == "button":
                        print(f"[{label}] clicking '{txt}' via text")
                        cand.click(timeout=5000)
                        return True
            except Exception:
                pass
        time.sleep(1)
    return False


def _wait_for_dashboard(page, base_host: str, timeout: int = 30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        host = _host_name(page)
        if host == base_host:
            try:
                path = (urlparse(page.url).path or "").rstrip("/")
                if path not in ("/login", "/login/sso", "/signup", ""):
                    return True
            except Exception:
                pass
        time.sleep(1)
    return False


def _find_workspace(page, base_host: str) -> str:
    url = page.url or ""
    parts = url.split("/")
    for part in reversed(parts):
        part = part.split("?")[0].strip()
        if part and part not in ("https:", "http:", base_host, "apps", "endpoints", "settings", "main", "onboarding"):
            if "." not in part and len(part) > 1:
                return part
    return ""


def create_endpoint(context, page, base_host: str, workspace: str, model: str, timeout: int) -> dict:
    result = {"baseUrl": "", "apiKey": "", "endpointName": "", "error": ""}

    env = "main"
    endpoints_url = f"https://{base_host}/endpoints/{workspace}/{env}/create"
    print(f"[endpoint] navigating to {endpoints_url}")
    page.goto(endpoints_url, wait_until="domcontentloaded", timeout=60_000)
    time.sleep(3)

    # Click "Shared" radio option
    shared_clicked = _click_text_button(page, ["Shared", "shared"], label="endpoint-shared", timeout=15)
    if not shared_clicked:
        shared_selectors = [
            "label:has-text('Shared')",
            "input[value='shared']",
            "[data-value='shared']",
            "input[name='endpoint-type'][value='shared']",
            "label:has(input[value='shared'])",
        ]
        el = _first_visible(page, shared_selectors, timeout=10)
        if el:
            try:
                el.click(timeout=5000)
                shared_clicked = True
                print("[endpoint] clicked Shared radio")
            except Exception:
                pass
    if not shared_clicked:
        result["error"] = "could not click 'Shared' endpoint type"
        return result
    time.sleep(2)

    # Select model - search and pick from dropdown
    print(f"[endpoint] selecting model: {model}")
    model_search_selectors = [
        'input[placeholder*="Search model"]',
        'input[placeholder*="search"]',
        'input[type="search"]',
        'input[role="combobox"]',
        "[data-slot='model-search'] input",
    ]
    search_el = _first_visible(page, model_search_selectors, timeout=10)
    if search_el:
        try:
            search_el.fill(model)
            print(f"[endpoint] typed '{model}' in search")
            time.sleep(2)
        except Exception:
            search_el.click()
            time.sleep(1)
            page.keyboard.type(model)
            time.sleep(2)
    else:
        # Try clicking the model dropdown/select first
        _click_text_button(page, ["Select model", "Choose model", "Model", "Pick a model"], label="model-dropdown", timeout=8)
        time.sleep(1)
        search_el = _first_visible(page, model_search_selectors, timeout=8)
        if search_el:
            search_el.fill(model)
            time.sleep(2)

    # Click the matching model option from the dropdown
    model_option_texts = [model, "Kimi K3", "kimi-k3", "Kimi-K3"]
    model_clicked = False
    for txt in model_option_texts:
        try:
            opts = page.locator(f"[role='option']:has-text('{txt}'), button:has-text('{txt}'), li:has-text('{txt}')").all()
            for opt in opts:
                if opt.is_visible():
                    opt.click(timeout=5000)
                    model_clicked = True
                    print(f"[endpoint] selected model option '{txt}'")
                    break
            if model_clicked:
                break
        except Exception:
            continue
    if not model_clicked:
        print("[endpoint] model dropdown option not found, trying get_by_text")
        for txt in model_option_texts:
            try:
                cand = page.get_by_text(txt, exact=False).first
                if cand.count() > 0 and cand.is_visible():
                    cand.click(timeout=5000)
                    model_clicked = True
                    print(f"[endpoint] selected model via text '{txt}'")
                    break
            except Exception:
                continue

    time.sleep(2)

    # Click "Create Endpoint" button
    create_clicked = _click_text_button(page, ["Create Endpoint", "Create endpoint", "Create"], label="create-endpoint", timeout=15)
    if not create_clicked:
        result["error"] = "could not click 'Create Endpoint' button"
        return result
    print("[endpoint] clicked Create Endpoint, waiting for creation...")
    time.sleep(5)

    # Wait for endpoint to be created - look for the base URL / API key on the page
    deadline = time.time() + timeout
    base_url_found = ""
    api_key_found = ""
    while time.time() < deadline:
        try:
            page_text = page.inner_text("body") or ""
        except Exception:
            page_text = ""

        # Look for base URL pattern (modal.com/v1/... or similar)
        import re
        url_matches = re.findall(r'https://[a-z0-9.-]+modal\.com/v1/[a-zA-Z0-9_-]+', page_text)
        if not base_url_found and url_matches:
            base_url_found = url_matches[0]
            print(f"[endpoint] found base URL: {base_url_found}")

        # Look for API key pattern
        key_matches = re.findall(r'(?:sk-|ak-|modal-)[a-zA-Z0-9]{20,}', page_text)
        if not api_key_found and key_matches:
            api_key_found = key_matches[0]
            print(f"[endpoint] found API key: {api_key_found[:10]}...")

        # Also check for a "Create Key" button in the Authentication section
        if not api_key_found:
            if _click_text_button(page, ["Create Key", "Create key", "Generate Key", "Create API Key", "Create Token", "Create token"], label="create-key", timeout=3):
                print("[endpoint] clicked Create Key")
                time.sleep(3)
                continue

        if base_url_found and api_key_found:
            break

        # Check if we got redirected to the endpoint detail page
        current_path = urlparse(page.url).path or ""
        if "/endpoints/" in current_path and "/create" not in current_path:
            print(f"[endpoint] redirected to endpoint detail: {page.url}")
            # The base URL and key might be on the detail page
            time.sleep(3)

        time.sleep(2)

    result["baseUrl"] = base_url_found
    result["apiKey"] = api_key_found
    if not base_url_found and not api_key_found:
        result["error"] = "endpoint may have been created but base URL / API key not found on page"
    return result


def get_stripe_link(context, page, base_host: str, workspace: str, timeout: int) -> dict:
    result = {"stripeUrl": "", "error": ""}

    # Navigate to billing/credits page
    credits_url = f"https://{base_host}/settings/{workspace}/billing?tab=credits"
    print(f"[stripe] navigating to {credits_url}")
    page.goto(credits_url, wait_until="domcontentloaded", timeout=60_000)
    time.sleep(3)

    # Look for "Add a payment method" or "Add payment" link/button
    deadline = time.time() + timeout
    while time.time() < deadline:
        # Try clicking "Add a payment method"
        clicked = _click_text_button(page, ["Add a payment method", "Add payment method", "Add payment", "Add a Payment Method"], label="add-payment", timeout=8)
        if clicked:
            print("[stripe] clicked Add a payment method, waiting for redirect...")
            time.sleep(3)
            # Check if we got redirected to Stripe
            current_url = page.url or ""
            if "stripe.com" in current_url or "checkout.stripe.com" in current_url:
                result["stripeUrl"] = current_url
                print(f"[stripe] captured Stripe URL: {current_url}")
                return result
            # The page might open a Stripe iframe or redirect via API
            time.sleep(2)

        # Check all links on the page for Stripe URLs
        try:
            links = page.locator("a[href*='stripe'], a[href*='add-payment-method']").all()
            for link in links:
                try:
                    href = link.get_attribute("href") or ""
                    if "add-payment-method" in href or "stripe" in href:
                        full_url = href if href.startswith("http") else f"https://{base_host}{href}"
                        print(f"[stripe] found payment link: {full_url}")
                        result["stripeUrl"] = full_url
                        return result
                except Exception:
                    continue
        except Exception:
            pass

        # Try navigating directly to the add-payment-method API endpoint
        api_url = f"https://{base_host}/api/stripe/{workspace}/add-payment-method"
        try:
            page.goto(api_url, wait_until="domcontentloaded", timeout=30_000)
            time.sleep(3)
            current_url = page.url or ""
            if "stripe.com" in current_url:
                result["stripeUrl"] = current_url
                print(f"[stripe] captured Stripe URL via direct nav: {current_url}")
                return result
        except Exception as e:
            print(f"[stripe] direct nav to add-payment-method failed: {e}")

        time.sleep(2)

    result["error"] = "could not find Stripe payment link"
    return result


def run(args: argparse.Namespace) -> int:
    base_url = args.base_url.rstrip("/") or DEFAULT_BASE_URL
    base_host = (urlparse(base_url).hostname or "").lower()
    if not base_host:
        base_host = urlparse(DEFAULT_BASE_URL).hostname

    user_data_dir = tempfile.mkdtemp(prefix="modal_setup_")
    launch_kwargs = dict(
        headless=args.headless,
        os=os.environ.get("CAMOUFOX_OS", "windows"),
        locale=os.environ.get("CAMOUFOX_LOCALE", "zh-CN"),
        humanize=False,
        block_webrtc=True,
        persistent_context=True,
        user_data_dir=user_data_dir,
        ignore_https_errors=True,
        viewport={"width": 1280, "height": 800},
        firefox_user_prefs={
            "permissions.default.image": 2,
            "permissions.default.media": 2,
            "media.autoplay.default": 5,
            "browser.cache.disk.enable": False,
            "browser.sessionstore.resume_from_crash": False,
        },
    )
    pw_proxy = _playwright_proxy(args.proxy)
    if pw_proxy:
        launch_kwargs["proxy"] = pw_proxy

    print(f"[browser] launching Camoufox for setup (headless={args.headless})")
    result = {"endpoint": {}, "stripe": {}, "error": ""}
    try:
        with Camoufox(**launch_kwargs) as context:
            page = context.pages[0] if context.pages else context.new_page()
            page.add_init_script("""
                (() => {
                    const swallow = (e) => { e.preventDefault(); e.stopPropagation(); };
                    window.addEventListener('error', swallow, true);
                    window.addEventListener('unhandledrejection', swallow, true);
                    window.onerror = function() { return true; };
                })();
            """)
            page.on("pageerror", lambda err: print(f"[browser] pageerror swallowed: {err}"))

            # Navigate to the base host first (about:blank can't set domain cookies),
            # then inject cookies and reload.
            page.goto(f"https://{base_host}/login", wait_until="domcontentloaded", timeout=60_000)
            time.sleep(2)
            _set_cookies(context, args.cookie, base_host)
            print(f"[setup] injected cookies for .{base_host}")
            page.goto(f"https://{base_host}/", wait_until="domcontentloaded", timeout=60_000)
            time.sleep(3)

            if not _wait_for_dashboard(page, base_host, timeout=20):
                result["error"] = "cookie did not authenticate; please re-login"
                _write_result(args.out, False, **result)
                return 1

            workspace = _find_workspace(page, base_host)
            if not workspace:
                result["error"] = "could not determine workspace from dashboard URL"
                _write_result(args.out, False, **result)
                return 1
            print(f"[setup] workspace: {workspace}")

            # Task 1: Create endpoint
            print("[setup] === Task 1: Create Endpoint ===")
            endpoint_result = create_endpoint(context, page, base_host, workspace, args.model, args.timeout)
            result["endpoint"] = endpoint_result

            # Task 2: Get Stripe link
            print("[setup] === Task 2: Get Stripe Link ===")
            stripe_result = get_stripe_link(context, page, base_host, workspace, 60)
            result["stripe"] = stripe_result

            result["workspace"] = workspace
            _write_result(args.out, True, **result)
            print(f"[result] ok endpoint_base={endpoint_result.get('baseUrl', '')} stripe={stripe_result.get('stripeUrl', '')}")
            return 0
    except Exception as e:
        print(f"[error] {e}")
        result["error"] = str(e)
        _write_result(args.out, False, **result)
        return 1
    finally:
        shutil.rmtree(user_data_dir, ignore_errors=True)


def _write_result(out_path: str, ok: bool, **fields) -> None:
    payload = {"ok": ok}
    payload.update(fields)
    try:
        with open(out_path, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False)
    except Exception as e:
        print(f"[result] failed to write {out_path}: {e}")


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
