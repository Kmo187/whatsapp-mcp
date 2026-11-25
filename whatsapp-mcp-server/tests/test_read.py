from __future__ import annotations

import json
import sqlite3
from dataclasses import replace
from pathlib import Path

from conftest import ALICE, BOB, GROUP, OLD_SCHEMA, build_db

import whatsapp as wa


def test_list_messages_newest_first_across_timezones(env):
    out = wa.list_messages(chat_jid=ALICE, include_context=False)
    lines = [line for line in out.splitlines() if "id=" in line]
    ids = [line.split("id=")[1].split(" ")[0].rstrip("]") for line in lines]
    assert ids == ["a4", "a3", "a2", "a1"], ids


def test_untrusted_content_is_flagged(env):
    out = wa.list_messages(query="instructions", include_context=False)
    assert out.startswith(wa.UNTRUSTED_NOTE)
    assert "id=a4" in out


def test_multi_word_and_phrase_search(env):
    out = wa.list_messages(query="board game", include_context=False)
    assert "id=a3" in out and "id=a1" not in out
    out = wa.list_messages(query='"game board"', include_context=False)
    assert out == "No messages to display." or "id=a3" not in out


def test_like_wildcards_are_escaped(env):
    assert wa.list_messages(query="100%", include_context=False) == "No messages to display."


def test_context_has_no_duplicates(env):
    out = wa.list_messages(chat_jid=ALICE, limit=4, context_before=2, context_after=2)
    assert out.count("id=a2 ") + out.count("id=a2]") == 1


def test_sender_names_use_contacts_and_push_names(env):
    out = wa.list_messages(chat_jid=GROUP, include_context=False)
    assert "From: Carla Rossi" in out  # contacts table wins
    assert "From: Alice" in out  # chat name for a known direct chat
    assert "From: Me" in out
    assert "reactions: 👍" in out


def test_reply_reference_is_shown(env):
    assert "reply_to=a2" in wa.list_messages(chat_jid=ALICE, query="board", include_context=False)


def test_after_filter_uses_real_time(env):
    after = (env["now"] - __import__("datetime").timedelta(minutes=15)).isoformat()
    out = wa.list_messages(after=after, include_context=False)
    assert "id=a3" in out and "id=g2" in out and "id=a1" not in out and "id=g1" not in out


def test_list_chats_sorted_and_last_message(env):
    chats = wa.list_chats()
    assert [c.jid for c in chats][:2] == [GROUP, ALICE]
    group = chats[0].to_dict()
    assert group["is_group"] and group["last_message"] == "Count me in" and group["last_is_from_me"] is True
    assert [c.jid for c in wa.list_chats(chat_type="direct")] == [ALICE, f"{BOB}@s.whatsapp.net"]


def test_direct_chat_lookup_is_exact(env):
    assert wa.get_direct_chat_by_contact("+41 79 000 00 01").jid == ALICE
    assert wa.get_direct_chat_by_contact("0041790000002").name == "Bob"
    assert wa.get_direct_chat_by_contact("4179") is None


def test_search_contacts(env):
    names = {c.name for c in wa.search_contacts("carla")}
    assert "Carla Rossi" in names
    assert any(c.jid == ALICE for c in wa.search_contacts("0000001"))


def test_message_context(env):
    ctx = wa.get_message_context("a3", before=2, after=1)
    assert [m.id for m in ctx.before] == ["a1", "a2"]
    assert [m.id for m in ctx.after] == ["a4"]


def test_catch_up_flags_awaiting_reply(env):
    res = wa.catch_up(hours=2)
    names = [c["chat_name"] for c in res["chats"]]
    assert names == ["Climbing Crew", "Alice"]
    assert res["awaiting_reply"] == ["Alice"]
    alice = res["chats"][1]
    assert alice["recent_messages"][-1]["id"] == "a4"
    assert res["total_messages"] == 7
    assert "Bob" not in names
    assert wa.catch_up(hours=48, include_groups=False)["active_chats"] == 2


def test_chat_stats(env):
    s = wa.chat_stats(days=7)
    assert s["total_messages"] == 8 and s["sent_by_me"] == 2
    assert s["media"] == {"document": 1}
    assert s["most_active_chats"][0]["chat"] in {"Alice", "Climbing Crew"}
    g = wa.chat_stats(chat_jid=GROUP)
    assert g["total_messages"] == 3 and "most_active_chats" not in g


def test_export_markdown_and_json(env):
    md = wa.export_chat(GROUP)
    text = Path(md["path"]).read_text(encoding="utf-8")
    assert md["messages"] == 3 and "# Climbing Crew" in text and "Carla Rossi" in text
    js = wa.export_chat(ALICE, fmt="json")
    data = json.loads(Path(js["path"]).read_text(encoding="utf-8"))
    assert [m["id"] for m in data["messages"]] == ["a1", "a2", "a3", "a4"]


def test_database_is_opened_read_only(env):
    with wa._db() as conn:
        try:
            conn.execute("DELETE FROM messages")
        except sqlite3.OperationalError as exc:
            assert "readonly" in str(exc).lower()
        else:  # pragma: no cover
            raise AssertionError("database must be read-only")


def test_works_with_old_bridge_schema(tmp_path, env):
    db = tmp_path / "old.db"
    build_db(db, OLD_SCHEMA)
    wa._schema_cache.clear()
    wa.configure(replace(env["settings"], db_path=db), wa.bridge())
    out = wa.list_messages(chat_jid=GROUP, include_context=False)
    assert "id=g1" in out
    assert wa.catch_up(hours=2)["active_chats"] == 2
    assert wa.search_contacts("Alice")


def test_missing_database_gives_helpful_error(tmp_path, env):
    wa.configure(replace(env["settings"], db_path=tmp_path / "nope.db"), wa.bridge())
    try:
        wa.list_chats()
    except wa.DatabaseError as exc:
        assert "Start the WhatsApp bridge" in str(exc)
    else:  # pragma: no cover
        raise AssertionError
