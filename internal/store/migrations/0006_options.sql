-- Connection-string options for engines that take them (MongoDB's authSource, replicaSet, ...).
ALTER TABLE profiles ADD COLUMN options TEXT NOT NULL DEFAULT '';
