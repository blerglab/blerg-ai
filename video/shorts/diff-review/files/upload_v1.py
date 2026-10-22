"""Upload client: sends a photo to the storage service."""
import time


class UploadError(Exception):
    """The storage service refused or dropped an upload."""


RETRYABLE = (500, 502, 503, 504)


def upload(session, url, data, retries=5, base_delay=0.5, sleep=time.sleep):
    """PUT the photo bytes to url and return the stored object's id.

    A 5xx answer or a dropped connection is retried with exponential backoff.
    """
    for attempt in range(retries + 1):
        try:
            response = session.put(url, data=data, timeout=30)
        except ConnectionError:
            response = None
        if response is not None and response.status_code not in RETRYABLE:
            response.raise_for_status()
            return response.json()["id"]
        if attempt == retries:
            break
        delay = base_delay * 2 ** attempt
        sleep(delay)
    raise UploadError(f"gave up after {retries + 1} attempts")
