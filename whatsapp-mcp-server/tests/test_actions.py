from __future__ import annotations

from dataclasses import replace

from conftest import ALICE, GROUP

import whatsapp as wa


def test_send_message_passes_reply_and_token(env):
    res = wa.send_message("41790000001", "On my way", reply_to="a3")
    assert res["success"] and res["id"] == "NEWID"
    method, path, body = env["bridge"].calls[-1]
    assert (method, path) == ("POST", "/api/send")
    assert body == {"recipient": "41790000001", "message": "On my way", "reply_to": "a3"}


def test_read_only_mode_blocks_every_action(env):
    wa.configure(replace(env["settings"], read_only=True), wa.bridge())
    for res in (
        wa.send_message(ALICE, "x"),
        wa.send_reaction(ALICE, "a3", "👍"),
        wa.mark_as_read(ALICE),
        wa.schedule_message(ALICE, "x", "2030-01-01T09:00:00+01:00"),
    ):
        assert res["success"] is False and "read-only" in res["message"]
    assert env["bridge"].calls == []


def test_recipient_allow_list(env):
    wa.configure(replace(env["settings"], allowed_recipients=["+41 79 000 00 01", GROUP]), wa.bridge())
    assert wa.send_message("41790000001@s.whatsapp.net", "ok")["success"]
    assert wa.send_message(GROUP, "ok")["success"]
    blocked = wa.send_message("41790000002", "nope")
    assert not blocked["success"] and "ALLOWED_RECIPIENTS" in blocked["message"]


def test_media_dir_restriction(env, tmp_path):
    inside = env["media_dir"] / "photo.jpg"
    inside.write_bytes(b"\xff\xd8\xff")
    outside = tmp_path / "secret.txt"
    outside.write_text("private key")
    assert wa.send_file(ALICE, str(inside), caption="Look")["success"]
    assert env["bridge"].calls[-1][2]["message"] == "Look"
    res = wa.send_file(ALICE, str(outside))
    assert not res["success"] and "outside" in res["message"]
    sneaky = env["media_dir"] / ".." / "secret.txt"
    assert not wa.send_file(ALICE, str(sneaky))["success"]
    assert not wa.send_file(ALICE, "relative.jpg")["success"]


def test_empty_message_is_rejected(env):
    assert not wa.send_message(ALICE, "   ")["success"]


def test_bridge_errors_are_readable(env):
    env["bridge"].fail_with = (503, "not connected to WhatsApp")
    res = wa.send_message(ALICE, "hi")
    assert res == {"success": False, "message": "not connected to WhatsApp"}


def test_bridge_unreachable(env):
    import httpx

    from bridge import BridgeClient

    def boom(request):
        raise httpx.ConnectError("refused")

    wa.configure(env["settings"], BridgeClient(env["settings"], transport=httpx.MockTransport(boom)))
    status = wa.bridge_status()
    assert status["bridge"]["reachable"] is False and "Is it running" in status["bridge"]["error"]
    assert status["mcp_server"]["database_found"] is True


def test_schedule_converts_naive_time_to_local_offset(env):
    res = wa.schedule_message(ALICE, "Reminder", "2030-01-01T09:15:00")
    assert res["success"] and res["id"] == 7
    sent_at = env["bridge"].calls[-1][2]["send_at"]
    assert sent_at.startswith("2030-01-01T09:15:00") and ("+" in sent_at[10:] or "-" in sent_at[10:])
    assert not wa.schedule_message(ALICE, "x", "next friday")["success"]


def test_group_info_adds_names(env):
    res = wa.get_group_info(GROUP)
    assert res["group"]["participants"][0]["name"] == "Carla Rossi"


def test_cancel_and_list_schedule(env):
    wa.cancel_scheduled_message(7)
    wa.list_scheduled_messages("pending")
    assert env["bridge"].calls[-2][:2] == ("DELETE", "/api/schedule/7")
    assert env["bridge"].calls[-1][:2] == ("GET", "/api/schedule")
