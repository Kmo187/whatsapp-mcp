from guards import normalize_recipient


def test_normalize_recipient():
    assert normalize_recipient("+41 79 000-00-01") == "41790000001"
    assert normalize_recipient("0041790000001") == "41790000001"
    assert normalize_recipient("41790000001@s.whatsapp.net") == "41790000001"
    assert normalize_recipient("41790000001:12@s.whatsapp.net") == "41790000001"
    assert normalize_recipient("123@g.us") == "123@g.us"
