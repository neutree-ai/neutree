#!/usr/bin/env python3
"""Inject custom CSS theme panel into all Grafana dashboard JSON files.

Reads CSS from observability/grafana/theme/custom-theme.css and injects it
as a hidden text panel (id=9999) into every dashboard JSON under
observability/grafana/dashboards/.

Uses text-level manipulation to preserve original JSON formatting.
"""

import json
import os
import re
import glob

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.abspath(os.path.join(SCRIPT_DIR, "..", ".."))
CSS_FILE = os.path.join(REPO_ROOT, "observability", "grafana", "theme", "custom-theme.css")
DASHBOARDS_DIR = os.path.join(REPO_ROOT, "observability", "grafana", "dashboards")

THEME_PANEL_ID = 9999


def build_theme_panel(css_content: str) -> dict:
    """Build a Grafana text panel that injects CSS via <style> tag."""
    return {
        "id": THEME_PANEL_ID,
        "type": "text",
        "title": "",
        "gridPos": {"h": 1, "w": 24, "x": 0, "y": 0},
        "options": {
            "mode": "html",
            "content": "<style>\n" + css_content + "\n</style>",
            "code": {"language": "html", "showLineNumbers": False, "showMiniMap": False},
        },
        "transparent": True,
    }



def detect_indent(text: str) -> str:
    """Detect the indentation character used in the JSON file."""
    for line in text.split("\n"):
        if line.startswith("\t"):
            return "\t"
        if line.startswith("  "):
            # Count leading spaces
            stripped = line.lstrip(" ")
            n = len(line) - len(stripped)
            return " " * min(n, 4)
    return "\t"


def inject_theme(dashboard_path: str, css_content: str) -> bool:
    """Inject theme panel into a single dashboard JSON, preserving formatting."""
    with open(dashboard_path, "r") as f:
        text = f.read()

    # Parse to check existing theme panel
    dashboard = json.loads(text)
    panels = dashboard.get("panels", [])
    had_theme = any(p.get("id") == THEME_PANEL_ID for p in panels)
    panels_clean = [p for p in panels if p.get("id") != THEME_PANEL_ID]

    theme_panel = build_theme_panel(css_content)
    indent = detect_indent(text)

    if had_theme:
        existing = next(p for p in panels if p.get("id") == THEME_PANEL_ID)
        if existing == theme_panel:
            return False  # Already up-to-date, skip
        # CSS changed: full rewrite needed to replace the panel
        dashboard["panels"] = [theme_panel] + panels_clean
        with open(dashboard_path, "w") as f:
            json.dump(dashboard, f, indent=indent, ensure_ascii=False)
            f.write("\n")
        return True

    # Text-level injection: insert theme panel right after "panels": [
    panel_json = json.dumps(theme_panel, indent=indent, ensure_ascii=False)
    # Indent the panel JSON by 2 levels (inside "panels" array)
    indented_panel = "\n".join(
        indent * 2 + line if line.strip() else line
        for line in panel_json.split("\n")
    )

    panels_key_match = re.search(r'"panels"\s*:\s*\[', text)
    if not panels_key_match:
        # Fallback: full rewrite
        dashboard["panels"] = [theme_panel] + panels_clean
        with open(dashboard_path, "w") as f:
            json.dump(dashboard, f, indent=indent, ensure_ascii=False)
            f.write("\n")
        return True

    # Insert right after the opening [
    insert_pos = panels_key_match.end()
    before = text[:insert_pos]
    after = text[insert_pos:]

    # Add comma after the injected panel since existing panels follow
    new_text = before + "\n" + indented_panel + "," + after

    with open(dashboard_path, "w") as f:
        f.write(new_text)

    return True


def main():
    if not os.path.exists(CSS_FILE):
        print(f"❌ CSS file not found: {CSS_FILE}")
        return

    with open(CSS_FILE, "r") as f:
        css_content = f.read().strip()

    dashboard_files = sorted(glob.glob(os.path.join(DASHBOARDS_DIR, "*.json")))
    if not dashboard_files:
        print(f"❌ No dashboard JSON files found in {DASHBOARDS_DIR}")
        return

    print(f"🎨 Injecting theme into {len(dashboard_files)} dashboards...")
    for path in dashboard_files:
        name = os.path.basename(path)
        inject_theme(path, css_content)
        print(f"  ✅ {name}")

    print("🎨 Theme injection complete!")


if __name__ == "__main__":
    main()
