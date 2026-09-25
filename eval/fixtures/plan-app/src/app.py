def handle_request(payload):
    user_id = payload["user_id"]
    amount = payload["amount"]
    return charge(user_id, amount)


def charge(user_id, amount):
    return {"user_id": user_id, "charged": amount}
