"""A tiny Flask-style service with one existing endpoint.

Run with: pip install flask && python3 app.py
"""

from flask import Flask, jsonify

app = Flask(__name__)


@app.route("/users")
def users():
    return jsonify({"id": 1, "name": "ada"})


if __name__ == "__main__":
    app.run(port=8000)
