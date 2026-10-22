"""Albums: an ordered list of photo ids with a title."""
from dataclasses import dataclass, field


@dataclass
class Album:
    title: str
    photos: list = field(default_factory=list)

    def add(self, photo_id):
        if photo_id not in self.photos:
            self.photos.append(photo_id)

    def cover(self):
        return self.photos[0] if self.photos else None
