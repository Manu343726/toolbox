#!/usr/bin/env python3
"""Classify every method of every Toolbox contract from its name.

Every ConnectRPC method is a POST, so nothing about the transport says what
invoking one does. This writes the one thing that can: a @toolbox.side-effects
annotation in the method's own comment, which the gRPC parser reads back out of
the descriptor set and a policy then filters on.

The classification is derived from the method's leading verb, because that is
where the information is: a Get or List reads, a Put creates or replaces, an
Invoke or Generate reaches outside this process. A method the rules do not
recognise is left unannotated on purpose — an unclassified operation is one a
policy has to be told about, and guessing would be worse than not knowing.

The script is idempotent: a method that already carries an annotation is left
alone, so it can be re-run after a method is added.
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# (verb prefix, side effects), most specific first.
RULES = [
    (("Delete", "Deregister", "Remove"), ("delete",)),
    (("Put", "Register", "Create", "Update", "Add", "Set"), ("create", "update")),
    (("Stop", "Expose", "Hide"), ("update",)),
    (("Invoke", "Call", "Generate", "Complete", "Stream", "Serve"), ("external",)),
    (("Evaluate", "Validate", "Render", "Parse", "Describe"), ("read_only",)),
    (("Get", "List", "Search", "Health", "Check", "Heartbeat", "Ping", "Resolve", "Bind"), ("read_only",)),
]

# A uniformly read-only service states its effects once, on the service, rather
# than repeating the annotation on every method it declares.
SERVICE_EFFECTS = {
    "DocumentationService": "read_only",
    "PolicyService": "read_only",
}

ANNOTATION = "@toolbox.side-effects"


def classify(name: str):
    for verbs, effects in RULES:
        if any(name.startswith(verb) for verb in verbs):
            return list(effects)
    return []


def annotate_service(text: str, service: str, effects: str) -> str:
    pattern = re.compile(rf"^service {service} \{{$", re.MULTILINE)
    match = pattern.search(text)
    if not match:
        return text
    declaration = f"// {ANNOTATION} {effects}\n"
    window = text[max(0, match.start() - 300):match.start()]
    if declaration.strip() in window:
        return text
    return text[:match.start()] + declaration + text[match.start():]


def annotate_methods(text: str) -> tuple[str, int]:
    lines = text.split("\n")
    output: list[str] = []
    index = 0
    changed = 0
    while index < len(lines):
        line = lines[index]
        index += 1
        match = re.match(r"^(\s*)rpc (\w+)\(", line)
        if not match:
            output.append(line)
            continue
        indent, method = match.group(1), match.group(2)
        effects = classify(method)
        if not effects:
            output.append(line)
            continue
        # The comment block directly above the method, if it declared one already.
        start = len(output)
        while start > 0 and output[start - 1].strip().startswith("//"):
            start -= 1
        if start < len(output) and any(ANNOTATION in entry for entry in output[start:]):
            output.append(line)
            continue
        output.insert(start, f"{indent}// {ANNOTATION} {' '.join(effects)}")
        output.append(line)
        changed += 1
    return "\n".join(output), changed


def main() -> int:
    total = 0
    paths = sorted(ROOT.glob("subsystems/*/proto/*.proto")) + sorted(
        ROOT.glob("pkg/api/proto/toolbox/api/v1/*.proto")
    )
    for path in paths:
        original = path.read_text()
        text = original
        for service, effects in SERVICE_EFFECTS.items():
            text = annotate_service(text, service, effects)
        text, changed = annotate_methods(text)
        if text != original:
            path.write_text(text)
            print(f"{path.relative_to(ROOT)}: {changed} method declaration(s)")
            total += changed
    print(f"total: {total}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
