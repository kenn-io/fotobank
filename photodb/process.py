import os
import shutil

from photodb.common import Photo
from photodb.store import PhotoStore
import photodb.util as util


def ingest_path(path, store_path):
    store = PhotoStore(store_path)

    movie_path = os.path.join(store_path, 'movies')
    os.makedirs(movie_path, exist_ok=True)

    for path in util.discover_movies(path):
        _, tail = os.path.split(path)

        checksum = util.get_checksum(path)
        extension = util.get_file_extension(path)
        movie_dest = '.'.join((os.path.join(movie_path, checksum), extension))
        print('Copying {0} to {1}'.format(path, movie_dest))
        shutil.copy(path, movie_dest)

    for path in util.discover_photos(path):
        # if 'Thumbs' in path:
        #     print('Skipping {0}'.format(path))
        #     continue

        photo = Photo(path)
        if photo.is_valid:
            store.insert_photo(photo)
