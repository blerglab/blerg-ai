"""Upload client: sends a photo to the storage service."""


class UploadError(Exception):
    """The storage service refused or dropped an upload."""


def upload(session, url, data):
    """PUT the photo bytes to url and return the stored object's id."""
    response = session.put(url, data=data, timeout=30)
    if response.status_code >= 500:
        raise UploadError(f"storage answered {response.status_code}")
    response.raise_for_status()
    return response.json()["id"]
