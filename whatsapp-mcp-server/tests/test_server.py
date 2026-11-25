"""The MCP server itself: tools are registered, annotated and callable."""

from __future__ import annotations

import asyncio

import main


def test_tools_are_registered_with_annotations(env):
    tools = {t.name: t for t in asyncio.run(main.mcp.list_tools())}
    for legacy in [
        "search_contacts",
        "list_messages",
        "list_chats",
        "get_chat",
        "get_direct_chat_by_contact",
        "get_contact_chats",
        "get_last_interaction",
        "get_message_context",
        "send_message",
        "send_file",
        "send_audio_message",
        "download_media",
    ]:
        assert legacy in tools, f"existing tool {legacy} must keep working"
    for new in [
        "catch_up",
        "get_chat_stats",
        "export_chat",
        "get_bridge_status",
        "send_reaction",
        "mark_as_read",
        "schedule_message",
        "list_scheduled_messages",
        "cancel_scheduled_message",
        "list_groups",
        "get_group_info",
        "check_whatsapp_numbers",
        "request_chat_history",
    ]:
        assert new in tools
    assert tools["list_messages"].annotations.readOnlyHint is True
    assert tools["send_message"].annotations.readOnlyHint is False
    assert tools["send_message"].annotations.openWorldHint is True


def test_call_tool_end_to_end(env):
    result = asyncio.run(main.mcp.call_tool("catch_up", {"hours": 2}))
    text = result[0][0].text if isinstance(result, tuple) else result[0].text
    assert "Climbing Crew" in text and "awaiting_reply" in text


def test_prompts_exist():
    names = {p.name for p in asyncio.run(main.mcp.list_prompts())}
    assert {"whatsapp_daily_digest", "whatsapp_draft_reply", "whatsapp_find"} <= names
