#!/usr/bin/env python3
"""
Minimal NER HTTP service for RedisKG hybrid extraction.

Supports two backends:
  --backend gliner   (default) — GLiNER transformer model, no API calls
  --backend spacy    — spaCy NER model

Install:
  pip install flask
  # For GLiNER:
  pip install gliner
  # For spaCy:
  pip install spacy && python -m spacy download en_core_web_sm

Run:
  python scripts/ner_service.py --port 9000 --backend gliner
  python scripts/ner_service.py --port 9000 --backend spacy

Then start RedisKG with:
  ./rediskg --extraction-strategy hybrid --ner-url http://localhost:9000 ingest ./data/

Protocol:
  POST /ner  {"text": "..."} → {"entities": [{"text": "...", "start": 0, "end": 5, "label": "ORG"}]}
  GET /health → 200 OK
"""

import argparse
import json

from flask import Flask, request, jsonify

app = Flask(__name__)
ner_model = None


def load_gliner():
    from gliner import GLiNER
    model = GLiNER.from_pretrained("urchade/gliner_multi-v2.1")
    labels = ["person", "organization", "location", "product", "event", "facility"]

    def predict(text):
        entities = model.predict_entities(text, labels, threshold=0.4)
        return [
            {
                "text": ent["text"],
                "start": ent["start"],
                "end": ent["end"],
                "label": ent["label"].upper(),
            }
            for ent in entities
        ]

    return predict


def load_spacy():
    import spacy
    nlp = spacy.load("en_core_web_sm")

    def predict(text):
        doc = nlp(text)
        return [
            {
                "text": ent.text,
                "start": ent.start_char,
                "end": ent.end_char,
                "label": ent.label_,
            }
            for ent in doc.ents
        ]

    return predict


@app.route("/ner", methods=["POST"])
def ner():
    data = request.get_json(force=True)
    text = data.get("text", "")
    if not text:
        return jsonify({"entities": []})
    entities = ner_model(text)
    return jsonify({"entities": entities})


@app.route("/health", methods=["GET"])
def health():
    return "OK", 200


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="NER service for RedisKG")
    parser.add_argument("--port", type=int, default=9000)
    parser.add_argument("--backend", choices=["gliner", "spacy"], default="gliner")
    args = parser.parse_args()

    print(f"Loading {args.backend} model...")
    if args.backend == "gliner":
        ner_model = load_gliner()
    else:
        ner_model = load_spacy()
    print(f"NER service ready on port {args.port}")

    app.run(host="0.0.0.0", port=args.port)
