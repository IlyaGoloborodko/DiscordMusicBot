# The wake-word gate: vosk-server with the SMALL Russian model.
#
# Why not alphacep/kaldi-ru (the big model this used to run): measured on the
# same clips, both models produced identical transcripts and identical gate
# decisions — including the two documented traps, "моя машина сломалась" and
# "включи Машину времени", which the small model also kept as "машина" rather
# than pulling toward the name. The big one costs 6.79GB of image and 5.28GiB of
# resident RAM against 395MB and 222MiB here, which is the difference between
# fitting on a 2GB VPS and not.
#
# What the old note in CLAUDE.md actually measured was `phrase_list` — a CLOSED
# grammar, which snaps every utterance to the nearest listed phrase. That is a
# property of restricting the vocabulary, not of model size: this model's
# vocabulary is open, and it tells "машина" from "марина" like the big one.
#
# Falling back is one line in docker-compose.yml (image: alphacep/kaldi-ru).
FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends curl unzip \
    && rm -rf /var/lib/apt/lists/*

# websockets<14 is required, not cosmetic: from 14 the handler signature lost its
# `path` argument, and vosk-server's asr_server.py still takes it. With a current
# websockets every connection dies as "close 1011 (internal server error)" and
# the gate looks like a network fault.
RUN pip install --no-cache-dir vosk "websockets<14"

WORKDIR /opt
RUN curl -fsSL -o model.zip https://alphacephei.com/vosk/models/vosk-model-small-ru-0.22.zip \
    && unzip -q model.zip && rm model.zip && mv vosk-model-small-ru-0.22 model

RUN curl -fsSL -o /opt/asr_server.py \
    https://raw.githubusercontent.com/alphacep/vosk-server/master/websocket/asr_server.py

ENV VOSK_MODEL_PATH=/opt/model \
    VOSK_SAMPLE_RATE=16000

EXPOSE 2700

CMD ["python", "/opt/asr_server.py"]
