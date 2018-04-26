import os
import shutil

from photodb.common import Photo
from photodb.store import PhotoStore
import photodb.util as util


if __name__ == '__main__':
    store_path = '/home/wesm/fotobank'
    store = PhotoStore(store_path)
    # to_ingest = '/home/wesm/Documents/photos_to_sort/PhotosTigerSpain'
    # to_ingest = '/media/wesm/6263-3532'
    # to_ingest = '/media/wesm/CANON_DC'
    # to_ingest = '/media/wesm/disk'
    # to_ingest = '/home/wesm/Documents/photos_to_sort'
    to_ingest = '/media/wesm/photos/Google Photos'

    movie_path = os.path.join(store_path, 'movies')
    os.makedirs(movie_path, exist_ok=True)

    for path in util.discover_movies(to_ingest):
        _, tail = os.path.split(path)

        checksum = util.get_checksum(path)
        extension = util.get_file_extension(path)
        movie_dest = '.'.join((os.path.join(movie_path, checksum), extension))
        print('Copying {0} to {1}'.format(path, movie_dest))
        shutil.copy(path, movie_dest)

    # for path in util.discover_photos(to_ingest):
    #     if 'Thumbs' in path:
    #         print('Skipping {0}'.format(path))
    #         continue

    #     photo = Photo(path)
    #     if photo.is_valid:
    #         store.insert_photo(photo)
