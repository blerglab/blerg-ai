#!/usr/bin/env python3
"""Convert review-rounds.csv to blerg-runner mark --import JSONL, without guessing missing data.

Expected columns: at,summary,engine,target,round,verdict,high,medium,low.
Use --column FIELD=HEADER for a differently named CSV column. --card and --board
supply the identity when the source has no card/board_id columns. All rows are
validated before any output is written. Dates must include the actual timezone.
"""
import argparse
import csv
import datetime
import json
import re
import sys


def convert(rows, mapping, board=None, card=None):
    out = []
    for line, row in enumerate(rows, 2):
        def value(name, default=""):
            return row.get(mapping.get(name, name), default).strip()
        try:
            at = value("at")
            when = datetime.datetime.fromisoformat(at.replace("Z", "+00:00"))
            if when.tzinfo is None:
                raise ValueError("at needs a timezone")
            verdict = value("verdict").lower()
            if verdict not in ("approved", "changes", "rejected"):
                raise ValueError("verdict must be approved, changes or rejected")
            target = value("target").lower()
            if not re.fullmatch(r"[a-f0-9]{7,40}", target):
                raise ValueError("target must be the reviewed commit sha")
            engine = value("engine")
            summary = value("summary")
            board_id = board or value("board_id")
            number = card if card is not None else int(value("card"))
            round_number = int(value("round"))
            if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", engine) or not summary or not board_id or not 1 <= number <= 10000000 or not 1 <= round_number <= 10000:
                raise ValueError("need summary, engine, board, card and positive round")
            findings = []
            for severity in ("high", "medium", "low"):
                if mapping.get(severity, severity) not in row:
                    raise ValueError(f"missing {severity} count column; map it with --column")
                count = int(value(severity))
                if not 0 <= count <= 1000:
                    raise ValueError("finding count out of range")
                findings.extend([severity] * count)
            if len(findings) > 1000:
                raise ValueError("more than 1000 findings")
            out.append(dict(kind="review", at=when.isoformat(), text=summary, by=engine,
                            board_id=board_id, card=number, round=round_number,
                            target=target, verdict=verdict, findings=findings))
        except (ValueError, AttributeError) as exc:
            raise ValueError(f"CSV line {line}: {exc}") from exc
    return out


def convert_audit(rows, board=None):
    """Lossless tracker export; never fabricate timestamps or board identities."""
    out = []
    for line, row in enumerate(rows, 2):
        try:
            review_date = datetime.date.fromisoformat(row["date"])
            ref = row["card"]
            match = re.fullmatch(r"([A-Z]+)-([1-9][0-9]*)", ref)
            number = int(match[2]) if match else None
            findings = []
            for severity in ("high", "medium", "low"):
                count = int(row[severity])
                if not 0 <= count <= 1000:
                    raise ValueError("finding count out of range")
                findings.extend([severity] * count)
            if len(findings) > 1000:
                raise ValueError("more than 1000 findings")
            verdict = row["verdict"].lower()
            if verdict not in ("approved", "changes", "rejected"):
                raise ValueError("invalid verdict")
            target = row["target"]
            if not re.fullmatch(r"[a-fA-F0-9]{7,40}", target):
                raise ValueError("invalid target sha")
            round_number = int(row["round"])
            if not 1 <= round_number <= 10000:
                raise ValueError("invalid round")
            missing = ["actual timestamp and timezone"]
            if not board:
                missing.append("board_id")
            if number is None:
                missing.append("numeric card mapping")
            if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", row["reviewer"]):
                missing.append("reviewer engine mapping")
            if not row["notes"].strip():
                missing.append("summary")
            out.append(dict(kind="review", review_date=review_date.isoformat(),
                            timestamp_precision="date", at=None, card_ref=ref,
                            card=number, board_id=board, round=round_number,
                            by=row["reviewer"], reviewer_label=row["reviewer"],
                            builder=row["builder"], target=target.lower(),
                            verdict=verdict, text=row["notes"], findings=findings,
                            import_ready=False, unresolved=missing, source=dict(row)))
        except (KeyError, ValueError, TypeError) as exc:
            raise ValueError(f"CSV line {line}: {exc}") from exc
    return out


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("csv")
    p.add_argument("--audit", action="store_true", help="Lossless export of date-only tracker CSV; unknown values remain null, not mark-import-ready")
    p.add_argument("--board")
    p.add_argument("--card", type=int)
    p.add_argument("--column", action="append", default=[], metavar="FIELD=HEADER")
    args = p.parse_args()
    try:
        mapping = dict(pair.split("=", 1) for pair in args.column)
        with open(args.csv, newline="", encoding="utf-8-sig") as src:
            out = convert_audit(csv.DictReader(src), args.board) if args.audit else convert(csv.DictReader(src), mapping, args.board, args.card)
        for mark in out:
            print(json.dumps(mark))
    except (OSError, ValueError) as exc:
        p.exit(1, f"{exc}\n")


if __name__ == "__main__":
    main()
