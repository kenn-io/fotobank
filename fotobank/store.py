import datetime
import os
import shutil

from sqlalchemy.sql import select
import sqlalchemy as sa

from fotobank.common import Photo
import fotobank.util as util


class PhotoStore(object):

    def __init__(self, base_path, verbose=False):
        self.verbose = verbose

        self.base_path = base_path
        os.makedirs(self.base_path, exist_ok=True)

        self.movie_path = os.path.join(self.base_path, 'movies')
        os.makedirs(self.movie_path, exist_ok=True)

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

    def _log_verbose(self, msg):
        if self.verbose:
            print(msg)

    def _log_normal(self, msg):
        print(msg)

    def have_photo_already(self, checksum):
        t = self.table_photos
        stmt = select(t)
        stmt = stmt.where(t.c.checksum == checksum)

        result = self.con.execute(stmt)
        return result.rowcount > 0

    def delete_checksum(self, checksum, metadata_only=True):
        """
        Delete photo from database having indicated md5 checksum

        Parameters
        ----------
        checksum : string
        metadata_only : boolean, default False
            If True, only delete metadata for photo
        """
        if not metadata_only:
            raise NotImplementedError

        t = self.table_photos
        stmt = (t.delete()
                .where(t.c.checksum == checksum))
        self.con.execute(stmt)
        self.con.commit()

    def import_directory(self, path, dry_run=False, move=False):
        """
        Ingest all images and movies in indicated directory

        Parameters
        ----------

        """
        for movie_src in sorted(util.discover_movies(path)):
            _, tail = os.path.split(movie_src)

            checksum = util.get_checksum(movie_src)
            extension = util.get_file_extension(movie_src)
            movie_filename = '.'.join((checksum, extension))

            self._add_file(movie_src, self.movie_path, movie_filename,
                           dry_run=dry_run, move=move)

        for image_src in sorted(util.discover_photos(path)):
            photo = Photo(image_src)
            if photo.is_valid:
                self.insert_photo(photo, dry_run=dry_run, move=move)

    def insert_photo(self, photo, dry_run=False, move=False):
        """
        Insert image file into database (if it does not exist already), moving
        file if requested

        Parameters
        ----------
        photo : fotobank.BasePhoto
        move : boolean, default False
            If True, move image file, otherwise copy
        """
        checksum = photo.checksum

        if self.have_photo_already(checksum):
            self._log_verbose('Skipping duplicate {0}'.format(photo.path))
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

        if not dry_run:
            self.con.execute(ins)
            self.con.commit()
        self._add_file(photo.path, directory, unique_path, dry_run=dry_run,
                       move=move)

    def sync_metadata(self, dry_run=False):
        """
        Delete metadata records for images that have been removed from the
        database by some other means
        """
        t = self.table_photos
        stmt = select(t)

        checksums_to_delete = []

        def _get_sort_timestamp(x):
            if x is None:
                return datetime.datetime(1970, 1, 1)
            else:
                return x

        result = self.con.execute(stmt)
        rows = list(result.mappings())
        records = sorted(rows, key=lambda x: _get_sort_timestamp(x['timestamp']))

        # Cache directory listings to avoid repeated network calls
        dir_listings = {}
        
        def _get_directory_files(dir_path):
            if dir_path not in dir_listings:
                try:
                    if os.path.exists(dir_path):
                        dir_listings[dir_path] = set(os.listdir(dir_path))
                    else:
                        dir_listings[dir_path] = set()
                except OSError:
                    # Handle permission errors or other filesystem issues
                    dir_listings[dir_path] = set()
            return dir_listings[dir_path]

        for record in records:
            relative_path = get_store_path(record)
            full_path = os.path.join(self.base_path, relative_path)
            
            # Split into directory and filename
            dir_path, filename = os.path.split(full_path)
            
            # Check if file exists in the cached directory listing
            dir_files = _get_directory_files(dir_path)
            if filename not in dir_files:
                checksums_to_delete.append((record['checksum'], full_path))

        for checksum, path in checksums_to_delete:
            self._log_normal("Deleting metadata for {0} at {1}"
                             .format(checksum, path))
            if not dry_run:
                self.delete_checksum(checksum)

    def _add_file(self, source_abspath, directory, unique_path, dry_run=False,
                  move=False):
        self._ensure_directory_exists(directory)

        dest_abspath = os.path.join(directory, unique_path)

        file_action = shutil.move if move else shutil.copyfile
        action_name = 'Moving' if move else 'Copying'

        self._log_normal('{0} {1} to {2}'.format(action_name, source_abspath,
                                                 dest_abspath))
        if not dry_run:
            file_action(source_abspath, dest_abspath)

    def _ensure_directory_exists(self, directory):
        if not os.path.exists(directory):
            self._log_normal('Creating {0}'.format(directory))
            os.makedirs(directory)


def get_photo_path(base_path, photo):
    directory = os.path.join(base_path,
                             _directory_from_timestamp(photo.timestamp))

    if photo.timestamp is None:
        _, base_name = os.path.split(photo.path)
    else:
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


def get_store_path(metadata):
    file_path = metadata['path']
    directory = _directory_from_timestamp(metadata['timestamp'])
    return os.path.join(directory, file_path)


def _directory_from_timestamp(timestamp):
    if timestamp is None:
        directory = 'unknown_date'
    else:
        directory = str(timestamp.year)
    return directory
