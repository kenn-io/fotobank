from datetime import datetime
import json
import os
import subprocess

import dateutil.parser as date_parser

import fotobank.util as util


class ImageMetadata(object):
    """
    Simple EXIF-independent container for the image metadata we track
    """

    def __init__(self, **tags):
        self.tags = tags

    def __getattribute__(self, key):
        if key in self.tags:
            return self.tags[key]
        else:
            return object.__getattribute__(self, key)

    def __getitem__(self, key):
        return self.tags[key]

    def __repr__(self):
        return '\n'.join('{0}: {1}'.format(*v) for v in self.tags.items())


def _get_metadata_field(tag):
    @property
    def getter(self):
        return self.metadata[tag]
    return getter


class BasePhoto(object):

    def __init__(self, path, metadata, is_valid=True):
        self.path = path
        self.file_ext = util.get_file_extension(self.path)
        self.metadata_type = util.get_type_from_ext(self.file_ext)
        self.metadata = metadata
        self.is_valid = is_valid

    width = _get_metadata_field('width')
    height = _get_metadata_field('height')
    make = _get_metadata_field('make')
    model = _get_metadata_field('model')
    focal_length = _get_metadata_field('focal_length')
    iso = _get_metadata_field('iso')
    shutter_speed = _get_metadata_field('shutter_speed')
    aperture = _get_metadata_field('aperture')
    timestamp = _get_metadata_field('timestamp')


class Photo(object):
    """
    An image file (or movie) with embedded EXIF metadata
    """
    def __init__(self, path):
        try:
            metadata = read_exif(path)
            is_valid = True
            self.metadata_exc = None
        except subprocess.CalledProcessError as e:
            metadata = None
            is_valid = False
            self.metadata_exc = e

        BasePhoto.__init__(self, path, metadata, is_valid=is_valid)

    @property
    def checksum(self):
        return util.get_checksum(self.path)

    @property
    def size(self):
        return os.stat(self.path).st_size

    def edit_metadata(self, create_date=None, make=None, model=None,
                      new_path=None):
        """
        Modify EXIF metadata in place or create copy
        """
        raise NotImplementedError


class MockPhoto(BasePhoto):
    """
    For unit testing
    """
    def __init__(self, path, metadata, checksum, size):
        self._checksum = checksum
        self._size = size
        BasePhoto.__init__(self, path, metadata)

    @property
    def checksum(self):
        return self._checksum

    @property
    def size(self):
        return self._size


# ----------------------------------------------------------------------
# EXIF metadata processing


def read_exif(path):
    exiftool_output = subprocess.check_output(['exiftool', '-j', path])
    tags = json.loads(exiftool_output)[0]

    width, height = get_exif_dimensions(tags)

    clean_tags = {
        'width': width,
        'height': height,
        'make': get_exif_make(tags),
        'model': get_exif_model(tags),
        'focal_length': get_exif_focal_length(tags),
        'width': width,
        'height': height,
        'timestamp': get_exif_timestamp(tags),
        'iso': get_exif_iso(tags),
        'shutter_speed': get_exif_shutter(tags),
        'aperture': get_exif_aperture(tags)
    }

    return ImageMetadata(**clean_tags)


def get_exif_iso(tags):
    try:
        return tags['ISO']
    except KeyError:
        return -1


def get_exif_shutter(tags):
    if 'ShutterSpeedValue' in tags:
        return tags['ShutterSpeedValue']
    else:
        try:
            return tags['ShutterSpeed']
        except KeyError:
            return 'unknown'


def get_exif_aperture(tags):
    try:
        return tags['FNumber']
    except KeyError:
        return -1


def get_exif_dimensions(tags):
    if 'RawImageFullWidth' in tags:
        width = tags['RawImageFullWidth']
        height = tags['RawImageFullHeight']
    else:
        width = tags['ImageWidth']
        height = tags['ImageHeight']

    return width, height


def get_exif_make(tags):
    try:
        return tags['Make']
    except KeyError:
        return 'unknown'


def get_exif_model(tags):
    try:
        return tags['Model']
    except KeyError:
        return 'unknown'


def get_exif_focal_length(tags):
    try:
        return tags['FocalLength']
    except KeyError:
        return r'unknown'


_EXIF_TIMESTAMP_FORMAT = '%Y:%m:%d %H:%M:%S'


def get_exif_timestamp(tags):
    try:
        timestamp = tags['CreateDate']
    except KeyError:
        # Do not guess the image date if CreateDate is not present
        return None

    try:
        return datetime.strptime(timestamp, _EXIF_TIMESTAMP_FORMAT)
    except ValueError:
        return date_parser.parse(timestamp)
