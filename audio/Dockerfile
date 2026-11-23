FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app
COPY requirements.txt ./requirements.txt
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && pip install --no-cache-dir -r requirements.txt \
    && useradd --system --uid 10001 --no-create-home radio
COPY app ./app
COPY models ./models
RUN cd models && sha256sum -c yamnet.tflite.sha256
USER 10001:10001
EXPOSE 8790
CMD ["python", "-m", "app.service"]
