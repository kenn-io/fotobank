import os
import shutil

from fotobank.store import PhotoStore, get_store_path

from sqlalchemy.sql import select

store_path = '/media/wesm/photos/fotobank'
to_ingest = '/media/wesm/photos/Google Photos'

store = PhotoStore(store_path)

# from fotobank.process import ingest_path
# ingest_path(to_ingest, store_path)

t = store.table_photos
stmt = select([t])
stmt = stmt.where(t.c.original_filename.like('%Thumbs%'))

results = list(store.con.execute(stmt))

deadpool = os.path.join(store_path, 'deadpool')

os.makedirs(deadpool, exist_ok=True)

for meta in results:
    cs = meta['checksum']
    print("Deleting {0}".format(cs))
    store.delete_checksum(cs)
    file_relpath = get_store_path(meta)
    file_abspath = os.path.join(store_path, file_relpath)
    print("Moving {0} to {1}".format(file_abspath, deadpool))
    shutil.move(file_abspath, deadpool)
