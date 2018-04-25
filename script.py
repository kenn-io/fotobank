
from photodb.common import Photo
from photodb.store import PhotoStore
import photodb.util as util


if __name__ == '__main__':
    store = PhotoStore('/home/wesm/photos')
    # to_ingest = '/home/wesm/Documents/photos_to_sort/PhotosTigerSpain'
    to_ingest = '/media/wesm/6263-3532'

    for path in util.discover_photos(to_ingest):
        photo = Photo(path)
        if photo.is_valid:
            store.insert_photo(photo)
