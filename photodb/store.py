import json
import os
import shutil

from sqlalchemy.sql import select
import sqlalchemy as sa


class PhotoStore(object):

    def __init__(self, base_path):
        self.base_path = base_path

        if not os.path.exists(self.base_path):
            os.makedirs(self.base_path)

        self.registry_path = os.path.join(self.base_path, 'registry.sqlite')

        self.metadata = sa.MetaData()
        self.table_photos = sa.Table(
            'photos', self.metadata,
            sa.Column('path', sa.String),
            sa.Column('original_filename', sa.String),
            sa.Column('timestamp', sa.DateTime),
            sa.Column('size', sa.Integer),
            sa.Column('checksum', sa.String),
            sa.Column('make', sa.String),
            sa.Column('model', sa.String),
            sa.Column('width', sa.Integer),
            sa.Column('focal_length', sa.String),
            sa.Column('height', sa.Integer),
            sa.Column('iso', sa.Integer),
            sa.Column('shutter', sa.String),
            sa.Column('aperture', sa.Float))

        self.registry, self.con = self._open_registry()

    def _open_registry(self):
        engine = sa.create_engine('sqlite:///{0}'.format(self.registry_path))
        self.metadata.create_all(engine)
        return engine, engine.connect()

    def _log(self, msg):
        print(msg)

    def have_photo_already(self, checksum):
        t = self.table_photos
        stmt = select([t])
        stmt = stmt.where(t.c.checksum == checksum)

        results = list(self.con.execute(stmt))
        return len(results) > 0

    def insert_photo(self, photo):
        checksum = photo.checksum

        if self.have_photo_already(checksum):
            self._log('Skipping duplicate {0}'.format(photo.path))
            return

        directory, unique_path = get_photo_path(self.base_path, photo)

        ins = (self.table_photos.insert()
               .values(path=unique_path,
                       original_filename=photo.path,
                       timestamp=photo.timestamp,
                       size=photo.size,
                       make=photo.make,
                       model=photo.model,
                       checksum=checksum,
                       width=photo.width,
                       height=photo.height,
                       iso=photo.iso,
                       shutter=photo.shutter_speed,
                       aperture=photo.aperture))

        self.con.execute(ins)

        self._copy_to_store(photo.path, directory, unique_path)

    def _copy_to_store(self, source_abspath, directory, unique_path):
        self._ensure_directory_exists(directory)

        dest_abspath = os.path.join(directory, unique_path)
        self._log('Copying {0} to {1}'.format(source_abspath,
                                              dest_abspath))
        shutil.copy(source_abspath, dest_abspath)

    def _ensure_directory_exists(self, directory):
        if not os.path.exists(directory):
            self._log('Creating {0}'.format(directory))
            os.makedirs(directory)


def get_photo_path(base_path, photo):
    directory = os.path.join(base_path, str(photo.timestamp.year))

    base_name = photo.timestamp.strftime('%Y%m%d_%H%M%S')

    # There may be multiple photos taken in the same second, we
    # increment the sequence number until finding something unique
    seq = 0
    while True:
        unique_path = '.'.join(('_'.join((base_name, str(seq))),
                                photo.file_ext))

        abspath = os.path.join(directory, unique_path)

        if not os.path.exists(abspath):
            break
        seq += 1

    return directory, unique_path
